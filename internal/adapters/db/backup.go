package db

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/araihu/moco/db/migrations"
	"github.com/araihu/moco/internal/core/ports"
)

// SnapshotState describes a complete, immutable database snapshot. It contains
// key identifiers, never deployment root-key material.
type SnapshotState struct {
	SchemaVersion      int64    `json:"schemaVersion"`
	ResourceVersion    int64    `json:"resourceVersion"`
	ActiveRootKeyID    string   `json:"activeRootKeyId"`
	EncryptionKeyEpoch int64    `json:"encryptionKeyEpoch"`
	RequiredRootKeyIDs []string `json:"requiredRootKeyIds"`
}

// Snapshot writes a consistent copy including committed WAL transactions. The
// destination must be a new or empty private file owned by the caller. Unlike
// Open, an OpenReadOnly source neither migrates nor creates the live database.
func (s *Store) Snapshot(ctx context.Context, destination string) error {
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	_, err = s.database.ExecContext(ctx, "VACUUM main INTO ?", absolute)
	if err != nil {
		return fmt.Errorf("snapshot SQLite database: %w", err)
	}
	return nil
}

// InspectSnapshot checks SQLite integrity, foreign keys and the supported
// migration version. Call only against an isolated snapshot, never a live store.
func (s *Store) InspectSnapshot(ctx context.Context) (SnapshotState, error) {
	var state SnapshotState
	var dirty bool
	if err := s.database.QueryRowContext(ctx, "SELECT version, dirty FROM schema_migrations").Scan(&state.SchemaVersion, &dirty); err != nil {
		return state, fmt.Errorf("read snapshot schema: %w", err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return state, err
	}
	var latest int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		version, err := strconv.ParseInt(strings.SplitN(entry.Name(), "_", 2)[0], 10, 64)
		if err != nil {
			return state, err
		}
		latest = max(latest, version)
	}
	if dirty || state.SchemaVersion != latest {
		return state, errors.New("snapshot requires the matching Mocó schema version and a clean migration state")
	}
	var integrity string
	if err := s.database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return state, fmt.Errorf("check snapshot integrity: %w", err)
	}
	if integrity != "ok" {
		return state, errors.New("snapshot failed SQLite integrity check")
	}
	rows, err := s.database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return state, err
	}
	hasViolations := rows.Next()
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(rowsErr, closeErr); err != nil {
		return state, err
	}
	if hasViolations {
		return state, errors.New("snapshot failed SQLite foreign key check")
	}
	keyState, err := s.CurrentEncryptionKeyState(ctx)
	if err != nil {
		return state, err
	}
	state.ActiveRootKeyID, state.EncryptionKeyEpoch = keyState.ActiveRootKeyID, keyState.Epoch
	state.ResourceVersion, err = s.CurrentResourceVersion(ctx)
	if err != nil {
		return state, err
	}
	rows, err = s.database.QueryContext(ctx, `SELECT root_key_id FROM vault_keys
		UNION SELECT active_root_key_id FROM encryption_key_state WHERE active_root_key_id <> '' ORDER BY 1`)
	if err != nil {
		return state, err
	}
	defer func() { _ = rows.Close() }()
	state.RequiredRootKeyIDs = []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return state, err
		}
		state.RequiredRootKeyIDs = append(state.RequiredRootKeyIDs, id)
	}
	return state, rows.Err()
}

// BackupCipher authenticates both populated and empty vault data keys and can
// decrypt secrets during an offline recovery check.
type BackupCipher interface {
	ports.SecretCipher
	VerifyVaultKey(string, string, ports.WrappedVaultKey) error
}

// VerifySnapshotEncryption authenticates every wrapped data key and ciphertext,
// one row at a time. Plaintext is wiped and never written to disk or errors.
func (s *Store) VerifySnapshotEncryption(ctx context.Context, cipher BackupCipher) error {
	if err := s.verifySnapshotVaultKeys(ctx, cipher); err != nil {
		return err
	}
	return s.verifySnapshotSecrets(ctx, cipher)
}

func (s *Store) verifySnapshotVaultKeys(ctx context.Context, cipher BackupCipher) error {
	rows, err := s.database.QueryContext(ctx, "SELECT tenant_id, vault_id, root_key_id, salt, wrapped_key FROM vault_keys")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tenant, vault string
		var key ports.WrappedVaultKey
		if err := rows.Scan(&tenant, &vault, &key.RootKeyID, &key.Salt, &key.Ciphertext); err != nil {
			return err
		}
		if err := cipher.VerifyVaultKey(tenant, vault, key); err != nil {
			return fmt.Errorf("authenticate snapshot vault key: %w", err)
		}
	}
	return rows.Err()
}

func (s *Store) verifySnapshotSecrets(ctx context.Context, cipher BackupCipher) error {
	// A LEFT JOIN makes missing vault keys fail validation rather than silently
	// omitting those secrets (the schema only relates secrets to vaults).
	rows, err := s.database.QueryContext(ctx, `SELECT s.tenant_id, s.vault_id, s.path, s.digest, s.content_type,
		s.salt, s.ciphertext, k.root_key_id, k.salt, k.wrapped_key
		FROM secrets s LEFT JOIN vault_keys k USING (tenant_id, vault_id)`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var tenant, vault, path, digest, contentType string
		var key ports.WrappedVaultKey
		var value ports.EncryptedSecretValue
		if err := rows.Scan(&tenant, &vault, &path, &digest, &contentType, &value.Salt, &value.Ciphertext,
			&key.RootKeyID, &key.Salt, &key.Ciphertext); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("read snapshot secret or its vault key")
		}
		plaintext, err := cipher.DecryptSecret(key, tenant, vault, path, digest, contentType, value)
		if err != nil {
			return fmt.Errorf("authenticate snapshot secret: %w", err)
		}
		actual := sha256.Sum256(plaintext)
		clear(plaintext)
		runtime.KeepAlive(plaintext)
		if fmt.Sprintf("sha256:%x", actual) != digest {
			return errors.New("snapshot secret digest mismatch")
		}
	}
	return rows.Err()
}
