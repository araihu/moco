package operations

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// RuntimeOptions prepares owner-private copies of Kubernetes projected Secrets
// and private children of mounted volumes, without modifying the volume roots.
type RuntimeOptions struct {
	Source, Credentials, Data, Backups, Mirror, MirrorID string
}

// PrepareRuntime runs before either container starts. It never creates a mirror
// marker, generates credentials, or modifies database/archive contents.
func PrepareRuntime(options RuntimeOptions) error {
	paths := []string{options.Source, options.Credentials, options.Data, options.Backups, options.Mirror}
	for i, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) == "/" {
			return errors.New("runtime paths must be absolute non-root directories")
		}
		for _, other := range paths[:i] {
			if isWithin(path, other) || isWithin(other, path) {
				return errors.New("runtime directories must not overlap")
			}
		}
	}
	id, err := uuid.Parse(options.MirrorID)
	if err != nil || id == uuid.Nil {
		return errors.New("runtime preparation requires a nonzero mirror UUID")
	}
	// A CSI driver's fsGroup handling may have widened the provisioned marker's
	// mode. Verify its bounded content before restoring owner-only permissions.
	mirror, err := os.OpenRoot(options.Mirror)
	if err != nil {
		return errors.New("mirror must be provisioned before runtime preparation")
	}
	marker, err := readRuntimeFile(mirror, ".moco-destination", 128)
	_ = mirror.Close()
	if err != nil || strings.TrimSpace(string(marker)) != options.MirrorID {
		return errors.New("mirror marker is absent or incorrect; provision the off-host destination")
	}
	for _, path := range []string{options.Credentials, options.Data, options.Backups, options.Mirror} {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if err := secureRuntimeDirectory(path); err != nil {
			return err
		}
	}
	source, err := os.OpenRoot(options.Source)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	for _, name := range []string{"access.json", "cursor-key", "keyring.json", "metrics-token", "tls.crt", "tls.key", "ca.pem"} {
		data, err := readRuntimeFile(source, name, 1<<20)
		if err != nil {
			return fmt.Errorf("projected credential %s is missing, invalid or oversized", name)
		}
		stage, err := os.CreateTemp(options.Credentials, ".prepare-*")
		if err != nil {
			clear(data)
			return err
		}
		_, writeErr := stage.Write(data)
		clear(data)
		err = errors.Join(writeErr, stage.Close())
		if err == nil {
			err = os.Rename(stage.Name(), filepath.Join(options.Credentials, name))
		}
		if err != nil {
			_ = os.Remove(stage.Name())
			return err
		}
	}
	return nil
}

func readRuntimeFile(root *os.Root, name string, limit int64) ([]byte, error) {
	info, err := root.Stat(name)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("runtime input must be a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("runtime input must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		clear(data)
		return nil, errors.New("runtime input is empty or exceeds its byte limit")
	}
	return data, nil
}

func secureRuntimeDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return errors.New("runtime storage must be a real directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		} else if !entry.Type().IsRegular() {
			return errors.New("runtime storage must contain only regular files and directories")
		}
		return root.Chmod(name, mode)
	})
}
