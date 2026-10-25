// Package operations implements the single-host deployment's maintenance jobs.
package operations

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// ReadPrivateFile reads a bounded credential, rejecting links and public modes.
func ReadPrivateFile(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0) // #nosec G304 -- explicit deployment credential path; no symlink following.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("credential must be a regular file without group/world permissions")
	}
	payload, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(payload)) > limit {
		clear(payload)
		return nil, errors.New("credential cannot be read or exceeds its size limit")
	}
	return payload, nil
}

// Lock prevents concurrent server instances or maintenance jobs on one host.
// The lock file remains in place: unlinking it would allow a second inode lock.
func Lock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600) // #nosec G304 -- private deployment directory.
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("deployment operation is already running: %w", err)
	}
	return file, nil
}
