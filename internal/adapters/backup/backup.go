// Package backup creates and restores complete offline-verifiable SQLite
// recovery archives. Deployment credentials and root keys stay outside archives.
package backup

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
)

const (
	archiveFormat    = "moco.backup/v1"
	databaseName     = "database.sqlite"
	manifestName     = "manifest.json"
	maxManifestBytes = 64 << 10
)

// Manifest binds the snapshot bytes to its schema and required key eras. The
// checksum detects corruption; it is not a signature against malicious changes.
type Manifest struct {
	Format         string           `json:"format"`
	CreatedAt      time.Time        `json:"createdAt"`
	DatabaseBytes  int64            `json:"databaseBytes"`
	DatabaseSHA256 string           `json:"databaseSha256"`
	State          db.SnapshotState `json:"state"`
}

// Create captures a consistent live snapshot without migrating the source and
// publishes a single private archive atomically, without overwriting any file.
func Create(ctx context.Context, databasePath, outputPath string) (Manifest, error) {
	var manifest Manifest
	if err := requireNewPath(outputPath, false); err != nil {
		return manifest, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(outputPath), ".moco-backup-*")
	if err != nil {
		return manifest, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	snapshotPath := filepath.Join(stage, databaseName)
	file, err := newPrivateFile(snapshotPath)
	if err != nil {
		return manifest, err
	}
	if err := file.Close(); err != nil {
		return manifest, err
	}
	store, err := db.OpenReadOnly(ctx, databasePath)
	if err != nil {
		return manifest, fmt.Errorf("open backup source: %w", err)
	}
	// Record a conservative freshness boundary before SQLite establishes its
	// read transaction, rather than dating older data at the end of a long copy.
	manifest.CreatedAt = time.Now().UTC()
	snapshotErr := store.Snapshot(ctx, snapshotPath)
	if err := errors.Join(snapshotErr, store.Close()); err != nil {
		return manifest, err
	}
	manifest.State, err = inspect(ctx, snapshotPath, nil)
	if err != nil {
		return manifest, err
	}
	file, err = os.Open(snapshotPath) // #nosec G304 -- snapshot is inside our private staging directory.
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	manifest.DatabaseBytes, err = io.Copy(digest, contextReader{ctx, file})
	if err != nil {
		return manifest, err
	}
	manifest.DatabaseSHA256 = hex.EncodeToString(digest.Sum(nil))
	manifest.Format = archiveFormat
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return manifest, err
	}
	archivePath := filepath.Join(stage, "backup.tar")
	archive, err := newPrivateFile(archivePath)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = archive.Close() }()
	if err := writeArchive(ctx, archive, file, manifest); err != nil {
		return manifest, err
	}
	if err := errors.Join(archive.Sync(), archive.Close()); err != nil {
		return manifest, err
	}
	if err := ctx.Err(); err != nil {
		return manifest, err
	}
	return manifest, publish(archivePath, outputPath, false)
}

// Verify validates checksum, schema, integrity and manifest against an isolated
// database. A supplied keyring also authenticates every vault key and secret.
func Verify(ctx context.Context, archivePath string, keyring *encryption.Keyring) (Manifest, error) {
	stage, err := os.MkdirTemp("", ".moco-verify-*")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	return unpack(ctx, archivePath, stage, keyring)
}

// Restore verifies an archive including decryption before atomically publishing
// a new SQLite file. The destination must be offline, absent, and have no SQLite
// sidecars. Neither migrations nor encryption-epoch changes run during restore.
func Restore(ctx context.Context, archivePath, databasePath string, keyring *encryption.Keyring) (Manifest, error) {
	if keyring == nil {
		return Manifest{}, errors.New("restore requires a deployment keyring")
	}
	if err := requireNewPath(databasePath, true); err != nil {
		return Manifest{}, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(databasePath), ".moco-restore-*")
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	manifest, err := unpack(ctx, archivePath, stage, keyring)
	if err != nil {
		return Manifest{}, err
	}
	if err := ctx.Err(); err != nil {
		return Manifest{}, err
	}
	return manifest, publish(filepath.Join(stage, databaseName), databasePath, true)
}

func writeArchive(ctx context.Context, output io.Writer, database io.Reader, manifest Manifest) error {
	payload, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(payload) > maxManifestBytes {
		return errors.New("backup manifest exceeds 64 KiB")
	}
	writer := tar.NewWriter(output)
	if err := writer.WriteHeader(&tar.Header{Name: manifestName, Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	if err := writer.WriteHeader(&tar.Header{Name: databaseName, Mode: 0o600, Size: manifest.DatabaseBytes, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	if _, err := io.Copy(writer, contextReader{ctx, database}); err != nil {
		return err
	}
	return writer.Close()
}

func unpack(ctx context.Context, archivePath, stage string, keyring *encryption.Keyring) (Manifest, error) {
	var manifest Manifest
	archive, err := os.Open(archivePath) // #nosec G304 -- path is an explicit local recovery input.
	if err != nil {
		return manifest, err
	}
	defer func() { _ = archive.Close() }()
	info, err := archive.Stat()
	if err != nil {
		return manifest, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return manifest, errors.New("backup must be a regular file without group/world permissions")
	}
	reader := tar.NewReader(contextReader{ctx, archive})
	header, err := reader.Next()
	if err != nil {
		return manifest, fmt.Errorf("read backup manifest header: %w", err)
	}
	if header.Name != manifestName || header.Typeflag != tar.TypeReg || header.Size < 1 || header.Size > maxManifestBytes {
		return manifest, errors.New("invalid backup manifest entry")
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, fmt.Errorf("decode backup manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return manifest, errors.New("backup manifest must contain exactly one JSON value")
	}
	digestBytes, err := hex.DecodeString(manifest.DatabaseSHA256)
	if err != nil || len(digestBytes) != sha256.Size || hex.EncodeToString(digestBytes) != manifest.DatabaseSHA256 ||
		manifest.Format != archiveFormat || manifest.CreatedAt.IsZero() || manifest.DatabaseBytes < 1 || manifest.DatabaseBytes > info.Size() {
		return manifest, errors.New("invalid backup manifest format, size or checksum")
	}
	header, err = reader.Next()
	if err != nil {
		return manifest, fmt.Errorf("read backup database header: %w", err)
	}
	if header.Name != databaseName || header.Typeflag != tar.TypeReg || header.Size != manifest.DatabaseBytes {
		return manifest, errors.New("invalid backup database entry")
	}
	snapshotPath := filepath.Join(stage, databaseName)
	file, err := newPrivateFile(snapshotPath)
	if err != nil {
		return manifest, err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	written, err := io.CopyN(io.MultiWriter(file, digest), reader, manifest.DatabaseBytes)
	if err != nil {
		return manifest, err
	}
	if written != manifest.DatabaseBytes || hex.EncodeToString(digest.Sum(nil)) != manifest.DatabaseSHA256 {
		return manifest, errors.New("backup database checksum or size mismatch")
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return manifest, err
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return manifest, errors.New("backup contains unexpected entries or invalid trailer")
	}
	// The v1 writer emits exactly two tar end blocks, without appended data.
	var trailing [1]byte
	if n, err := archive.Read(trailing[:]); n != 0 || !errors.Is(err, io.EOF) {
		return manifest, errors.New("backup contains trailing data")
	}
	state, err := inspect(ctx, snapshotPath, keyring)
	if err != nil {
		return manifest, err
	}
	if !reflect.DeepEqual(state, manifest.State) {
		return manifest, errors.New("backup database state does not match manifest")
	}
	return manifest, nil
}

func inspect(ctx context.Context, path string, keyring *encryption.Keyring) (db.SnapshotState, error) {
	store, err := db.OpenReadOnly(ctx, path)
	if err != nil {
		return db.SnapshotState{}, err
	}
	defer func() { _ = store.Close() }()
	state, err := store.InspectSnapshot(ctx)
	if err != nil || keyring == nil {
		return state, err
	}
	for _, id := range state.RequiredRootKeyIDs {
		if _, ok := keyring.Keys[id]; !ok {
			return state, fmt.Errorf("backup requires unavailable root key ID %q", id)
		}
	}
	cipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: keyring.ActiveKeyID, RootKeys: keyring.Keys})
	if err != nil {
		return state, err
	}
	defer cipher.Destroy()
	return state, store.VerifySnapshotEncryption(ctx, cipher)
}

func newPrivateFile(path string) (*os.File, error) {
	// #nosec G304 -- callers use fixed names in a freshly created private directory.
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

func requireNewPath(path string, sidecars bool) error {
	if path == "" || path == "-" || filepath.Clean(path) == "." {
		return errors.New("destination must name a new file")
	}
	suffixes := []string{""}
	if sidecars {
		suffixes = append(suffixes, "-wal", "-shm", "-journal")
	}
	for _, suffix := range suffixes {
		if _, err := os.Lstat(path + suffix); err == nil {
			return errors.New("destination or SQLite sidecar already exists; use a new offline path")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func publish(source, destination string, sidecars bool) error {
	if err := requireNewPath(destination, sidecars); err != nil {
		return err
	}
	// A same-filesystem hard link publishes the synced inode atomically and
	// cannot replace an existing file, including a racing file or symlink.
	if err := os.Link(source, destination); err != nil {
		return fmt.Errorf("publish recovery file without overwrite: %w", err)
	}
	directory, err := os.Open(filepath.Dir(destination)) // #nosec G304 -- explicit local output parent.
	if err != nil {
		return fmt.Errorf("file published but opening directory for sync failed: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("file published but directory sync failed: %w", err)
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
