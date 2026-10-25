package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/araihu/moco/internal/adapters/backup"
	"github.com/araihu/moco/internal/adapters/encryption"
	"github.com/google/uuid"
)

const StatusName = "status.json"

// BackupStatus is published only after the complete local + mirror job. Freshness
// uses the snapshot boundary, not the time a potentially slow copy finished.
type BackupStatus struct {
	LastAttempt     time.Time `json:"lastAttempt"`
	LastSuccess     time.Time `json:"lastSuccess"`
	SnapshotAt      time.Time `json:"snapshotAt"`
	Failures        uint64    `json:"failures"`
	LastAttemptOK   bool      `json:"lastAttemptOK"`
	DurationSeconds float64   `json:"durationSeconds"`
	Archive         string    `json:"archive"`
}

type BackupOptions struct {
	Database, Directory, MirrorDirectory, Keyring string
	MirrorID                                      string
	Keep                                          int
}

func privateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return errors.New("backup directories must be private directories (0700), not symlinks")
	}
	return nil
}

// RunBackup verifies decryption both locally and at the mirror before pruning.
// The mirror must be provisioned separately (e.g. an off-host mounted filesystem).
// Keys never accompany the archive, and failures preserve existing backups.
func RunBackup(ctx context.Context, options BackupOptions) (status BackupStatus, result error) {
	if options.Keep < 2 || !filepath.IsAbs(options.Database) || !filepath.IsAbs(options.Directory) || !filepath.IsAbs(options.MirrorDirectory) {
		return status, errors.New("backup requires absolute paths, a separate mirror, and retention of at least two archives")
	}
	local, mirror := filepath.Clean(options.Directory), filepath.Clean(options.MirrorDirectory)
	localInfo, err := os.Stat(local)
	if err != nil {
		return status, err
	}
	if local == mirror || isWithin(local, mirror) || isWithin(mirror, local) {
		return status, errors.New("backup mirror must be separate from the local archive directory")
	}
	if err := privateDirectory(local); err != nil {
		return status, err
	}
	lock, err := Lock(filepath.Join(local, ".backup.lock"))
	if err != nil {
		return status, err
	}
	defer func() { _ = lock.Close() }()
	statusPath := filepath.Join(local, StatusName)
	status, err = ReadBackupStatus(statusPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return status, err
	}
	started := time.Now().UTC()
	status.LastAttempt, status.LastAttemptOK = started, false
	defer func() {
		status.DurationSeconds = time.Since(started).Seconds()
		if result != nil {
			status.Failures++
		}
		result = errors.Join(result, writeStatus(statusPath, status))
	}()
	mirrorInfo, err := os.Stat(mirror)
	if err != nil {
		return status, err
	}
	if os.SameFile(localInfo, mirrorInfo) {
		return status, errors.New("backup mirror aliases the local archive directory")
	}
	if err := privateDirectory(mirror); err != nil {
		return status, err
	}
	marker, err := ReadPrivateFile(filepath.Join(mirror, ".moco-destination"), 128)
	if err != nil || options.MirrorID == "" || strings.TrimSpace(string(marker)) != options.MirrorID {
		return status, errors.New("backup mirror identity is absent or incorrect; verify the off-host mount")
	}
	payload, err := ReadPrivateFile(options.Keyring, 64<<10)
	if err != nil {
		return status, err
	}
	keyring, err := encryption.ParseKeyring(string(payload))
	clear(payload)
	if err != nil {
		return status, err
	}
	defer keyring.Destroy()
	name := "moco-" + started.Format("20060102T150405.000000000Z") + "-" + uuid.NewString() + ".tar"
	archive := filepath.Join(local, name)
	manifest, err := backup.Create(ctx, options.Database, archive)
	if err != nil {
		return status, err
	}
	if _, err := backup.Verify(ctx, archive, &keyring); err != nil {
		_ = os.Remove(archive)
		return status, err
	}
	mirrored := filepath.Join(mirror, name)
	if err := copyArchive(ctx, archive, mirrored); err != nil {
		return status, err
	}
	if _, err := backup.Verify(ctx, mirrored, &keyring); err != nil {
		_ = os.Remove(mirrored)
		return status, err
	}
	if err := prune(local, options.Keep); err != nil {
		return status, err
	}
	if err := prune(mirror, options.Keep); err != nil {
		return status, err
	}
	status.LastSuccess, status.SnapshotAt, status.LastAttemptOK, status.Archive = time.Now().UTC(), manifest.CreatedAt, true, name
	return status, nil
}

// InitializeMirror marks an explicitly provisioned destination. A detached host
// mount must not silently fall back to its unmarked local mountpoint directory.
func InitializeMirror(directory, id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil {
		return errors.New("mirror identity must be a nonzero UUID")
	}
	if err := privateDirectory(directory); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(directory, ".moco-destination"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- explicitly provisioned private mirror directory; never overwrite.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(id + "\n"); err != nil {
		return err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func isWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func copyArchive(ctx context.Context, source, destination string) error {
	input, err := os.Open(source) // #nosec G304 -- generated archive in private backup directory.
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	stage, err := os.CreateTemp(filepath.Dir(destination), ".moco-copy-*")
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close(); _ = os.Remove(stage.Name()) }()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := input.Read(buffer)
		if n > 0 {
			if _, err := stage.Write(buffer[:n]); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := errors.Join(stage.Sync(), stage.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Same-filesystem hard-link publication never overwrites a retained archive.
	if err := os.Link(stage.Name(), destination); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(destination))
}

var archiveName = regexp.MustCompile(`^moco-[0-9]{8}T[0-9]{6}\.[0-9]{9}Z-[0-9a-f-]{36}\.tar$`)

func prune(directory string, keep int) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if !archiveName.MatchString(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
			return errors.New("unsafe file in managed backup set")
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names[:max(0, len(names)-keep)] {
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}

func ReadBackupStatus(path string) (BackupStatus, error) {
	var status BackupStatus
	payload, err := ReadPrivateFile(path, 16<<10)
	if err != nil {
		return status, err
	}
	if err := json.Unmarshal(payload, &status); err != nil {
		return status, fmt.Errorf("invalid backup status: %w", err)
	}
	return status, nil
}

func writeStatus(path string, status BackupStatus) error {
	stage, err := os.CreateTemp(filepath.Dir(path), ".moco-status-*")
	if err != nil {
		return err
	}
	defer func() { _ = stage.Close(); _ = os.Remove(stage.Name()) }()
	if err := json.NewEncoder(stage).Encode(status); err != nil {
		return err
	}
	if err := errors.Join(stage.Sync(), stage.Close()); err != nil {
		return err
	}
	if err := os.Rename(stage.Name(), path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path) // #nosec G304 -- explicit deployment directory.
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
