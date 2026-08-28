package db

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/encryption"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestSecretPersistenceContainsOnlyCiphertextAndSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moco.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tenantID := "11111111-1111-4111-8111-111111111111"
	vaultID := "22222222-2222-4222-8222-222222222222"
	if _, err := store.CreateTenant(ctx, ports.CreateTenantCommand{
		Tenant: domain.Tenant{
			ID: tenantID, Name: "encrypted-at-rest", Labels: map[string]string{},
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
		ResponseETag: `"tenant-1"`,
	}); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := store.CreateVault(ctx, ports.CreateVaultCommand{
		Vault: domain.Vault{
			ID: vaultID, TenantID: tenantID, Name: "application", Labels: map[string]string{},
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
		ResponseETag: `"vault-1"`,
	}); err != nil {
		t.Fatalf("create vault: %v", err)
	}

	rootKey := bytes.Repeat([]byte{0x6b}, 32)
	cipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKey: rootKey})
	if err != nil {
		t.Fatalf("create envelope: %v", err)
	}
	service, err := services.NewSecretService(store, cipher, services.SecretServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
		Clock:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("create secret service: %v", err)
	}
	plaintext := []byte("ultra-secret-value-that-must-not-enter-sqlite")
	contentType := "text/plain"
	if _, _, err := service.Put(ctx, tenantID, vaultID, "prod/database/password", domain.SecretWrite{
		Value: plaintext, ContentType: &contentType,
	}, nil, nil); err != nil {
		t.Fatalf("put secret: %v", err)
	}

	var ciphertext, wrappedKey []byte
	if err := store.database.QueryRowContext(ctx, `
		SELECT secrets.ciphertext, vault_keys.wrapped_key
		FROM secrets
		JOIN vault_keys USING (tenant_id, vault_id)
		WHERE secrets.tenant_id = ? AND secrets.vault_id = ? AND secrets.path = ?
	`, tenantID, vaultID, "prod/database/password").Scan(&ciphertext, &wrappedKey); err != nil {
		t.Fatalf("read encrypted columns: %v", err)
	}
	if bytes.Contains(ciphertext, plaintext) || bytes.Contains(wrappedKey, plaintext) || bytes.Equal(ciphertext, plaintext) {
		t.Fatal("SQLite encrypted columns contain the plaintext value")
	}
	if bytes.Contains(wrappedKey, rootKey) || bytes.Equal(wrappedKey, rootKey) {
		t.Fatal("SQLite wrapped-key column contains the deployment root key")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close initial store: %v", err)
	}
	assertFilesDoNotContain(t, []string{databasePath, databasePath + "-wal"}, plaintext, []byte(base64.StdEncoding.EncodeToString(plaintext)), rootKey)

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restartedCipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKey: rootKey})
	if err != nil {
		t.Fatal(err)
	}
	restartedService, err := services.NewSecretService(reopened, restartedCipher, services.SecretServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := restartedService.Get(ctx, tenantID, vaultID, "prod/database/password")
	if err != nil {
		t.Fatalf("read after restart: %v", err)
	}
	if !bytes.Equal(secret.Value, plaintext) {
		t.Fatalf("restart changed plaintext: %q", secret.Value)
	}

	wrongCipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v1", RootKey: bytes.Repeat([]byte{0x7c}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongService, err := services.NewSecretService(reopened, wrongCipher, services.SecretServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := wrongService.Get(ctx, tenantID, vaultID, "prod/database/password"); err == nil {
		t.Fatal("wrong deployment root key unexpectedly decrypted persisted data")
	}
	if err := reopened.DeleteVault(ctx, tenantID, vaultID, nil, false); !errors.Is(err, ports.ErrResourceHasChildren) {
		t.Fatalf("delete non-empty vault without cascade: %v", err)
	}
	if err := reopened.DeleteVault(ctx, tenantID, vaultID, nil, true); err != nil {
		t.Fatalf("cascade encrypted vault: %v", err)
	}
	var childRows int
	if err := reopened.database.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM secrets) + (SELECT COUNT(*) FROM vault_keys)
	`).Scan(&childRows); err != nil {
		t.Fatalf("count cascaded secret rows: %v", err)
	}
	if childRows != 0 {
		t.Fatalf("cascade left %d secret or key rows", childRows)
	}
}

func assertFilesDoNotContain(t *testing.T, paths []string, forbidden ...[]byte) {
	t.Helper()
	for _, path := range paths {
		// #nosec G304 -- every path is derived from this test's private TempDir.
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, value := range forbidden {
			if bytes.Contains(content, value) {
				t.Fatalf("%s contains forbidden secret or root-key bytes", path)
			}
		}
	}
}
