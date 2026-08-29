package db

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/encryption"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestEncryptionKeyStateFencesStaleEpochs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state, err := store.CurrentEncryptionKeyState(ctx)
	if err != nil || state.Epoch != 0 || state.ActiveRootKeyID != "" {
		t.Fatalf("initial key state = %#v err=%v", state, err)
	}
	state, err = store.EnsureEncryptionKeyState(ctx, "root-v1", 1)
	if err != nil || state.ActiveRootKeyID != "root-v1" || state.Epoch != 1 {
		t.Fatalf("initial key epoch = %#v err=%v", state, err)
	}
	if _, err := store.EnsureEncryptionKeyState(ctx, "root-v1", 1); err != nil {
		t.Fatalf("same key epoch was not idempotent: %v", err)
	}
	state, err = store.EnsureEncryptionKeyState(ctx, "root-v2", 2)
	if err != nil || state.ActiveRootKeyID != "root-v2" || state.Epoch != 2 {
		t.Fatalf("advanced key epoch = %#v err=%v", state, err)
	}
	for _, stale := range []struct {
		id    string
		epoch int64
	}{
		{id: "root-v1", epoch: 1},
		{id: "other", epoch: 2},
		{id: "root-v3", epoch: 0},
	} {
		if _, err := store.EnsureEncryptionKeyState(ctx, stale.id, stale.epoch); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
			t.Fatalf("stale key state %v returned %v, want conflict", stale, err)
		}
	}
}

func TestStaleSecretServiceCannotWriteAfterEpochAdvance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tenantID := "33333333-3333-4333-8333-333333333333"
	vaultID := "44444444-4444-4444-8444-444444444444"
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if err := createRotationScope(ctx, store, tenantID, vaultID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureEncryptionKeyState(ctx, "root-v1", 1); err != nil {
		t.Fatal(err)
	}
	cipher, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKey: bytes.Repeat([]byte{0x71}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	service, err := services.NewSecretService(store, cipher, services.SecretServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"), KeyState: store,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnsureEncryptionKeyState(ctx, "root-v2", 2); err != nil {
		t.Fatal(err)
	}
	contentType := "text/plain"
	if _, _, err := service.Put(ctx, tenantID, vaultID, "prod/password", domain.SecretWrite{Value: []byte("value"), ContentType: &contentType}, nil, nil); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
		t.Fatalf("stale secret writer returned %v, want key-state conflict", err)
	}
}

func TestEncryptedPersistenceFencesEpochAtMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	tenantID := "55555555-5555-4555-8555-555555555555"
	vaultID := "66666666-6666-4666-8666-666666666666"
	if err := createRotationScope(ctx, store, tenantID, vaultID, now); err != nil {
		t.Fatal(err)
	}
	oldState := &ports.EncryptionKeyState{ActiveRootKeyID: "root-v1", Epoch: 1}
	if _, err := store.EnsureEncryptionKeyState(ctx, oldState.ActiveRootKeyID, oldState.Epoch); err != nil {
		t.Fatal(err)
	}
	oldEnvelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: oldState.ActiveRootKeyID, RootKey: bytes.Repeat([]byte{0x73}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := oldEnvelope.NewVaultKey(tenantID, vaultID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVaultKey(ctx, tenantID, vaultID, oldKey, oldState); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutSecret(ctx, ports.PutSecretCommand{
		TenantID: tenantID, VaultID: vaultID, Path: "prod/password", Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		ContentType: "text/plain", Value: ports.EncryptedSecretValue{Salt: bytes.Repeat([]byte{0x01}, 32), Ciphertext: bytes.Repeat([]byte{0x03}, 17)},
		UpdatedAt: now, CreateOnly: true, ExpectedKeyState: oldState,
	}); err != nil {
		t.Fatalf("initial fenced secret write: %v", err)
	}
	if _, err := store.EnsureEncryptionKeyState(ctx, "root-v2", 2); err != nil {
		t.Fatal(err)
	}
	newVaultID := "77777777-7777-4777-8777-777777777777"
	if _, err := store.CreateVault(ctx, ports.CreateVaultCommand{Vault: domain.Vault{
		ID: newVaultID, TenantID: tenantID, Name: "rotation-vault-2", Labels: map[string]string{},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, ResponseETag: `"vault-2"`}); err != nil {
		t.Fatal(err)
	}
	newCandidate, err := oldEnvelope.NewVaultKey(tenantID, newVaultID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVaultKey(ctx, tenantID, newVaultID, newCandidate, oldState); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
		t.Fatalf("stale fenced vault-key creation returned %v, want conflict", err)
	}
	if _, err := store.GetVaultKey(ctx, tenantID, newVaultID); !errors.Is(err, ports.ErrVaultKeyNotFound) {
		t.Fatalf("stale vault-key creation left a row: %v", err)
	}
	if _, err := store.PutSecret(ctx, ports.PutSecretCommand{
		TenantID: tenantID, VaultID: vaultID, Path: "prod/stale", Digest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		ContentType: "text/plain", Value: ports.EncryptedSecretValue{Salt: bytes.Repeat([]byte{0x02}, 32), Ciphertext: bytes.Repeat([]byte{0x04}, 17)},
		UpdatedAt: now, CreateOnly: true, ExpectedKeyState: oldState,
	}); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
		t.Fatalf("stale fenced secret write returned %v, want conflict", err)
	}
	if err := store.DeleteSecret(ctx, ports.DeleteSecretCommand{
		TenantID: tenantID, VaultID: vaultID, Path: "prod/password", ExpectedKeyState: oldState,
	}); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) {
		t.Fatalf("stale fenced secret delete returned %v, want conflict", err)
	}
	if _, err := store.GetSecretMetadata(ctx, tenantID, vaultID, "prod/password"); err != nil {
		t.Fatalf("stale delete removed the secret: %v", err)
	}
	replacement := oldKey
	replacement.RootKeyID = "root-v2"
	if changed, err := store.ReplaceVaultKey(ctx, ports.ReplaceVaultKeyCommand{
		TenantID: tenantID, VaultID: vaultID, Expected: oldKey, Replace: replacement, ExpectedKeyState: oldState,
	}); !errors.Is(err, ports.ErrEncryptionKeyStateConflict) || changed {
		t.Fatalf("stale fenced key replacement returned changed=%t err=%v, want conflict", changed, err)
	}
	current, err := store.GetVaultKey(ctx, tenantID, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if current.RootKeyID != oldState.ActiveRootKeyID {
		t.Fatalf("stale replacement changed root key ID to %q", current.RootKeyID)
	}
}

func TestVaultKeyRotationRewrapsWithoutChangingResourceVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tenantID := "11111111-1111-4111-8111-111111111111"
	vaultID := "22222222-2222-4222-8222-222222222222"
	if err := createRotationScope(ctx, store, tenantID, vaultID, now); err != nil {
		t.Fatal(err)
	}
	oldRoot := bytes.Repeat([]byte{0x51}, 32)
	activeRoot := bytes.Repeat([]byte{0x62}, 32)
	oldEnvelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKey: oldRoot})
	if err != nil {
		t.Fatal(err)
	}
	oldKey, err := oldEnvelope.NewVaultKey(tenantID, vaultID, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateVaultKey(ctx, tenantID, vaultID, oldKey, nil); err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("rotation-safe-secret")
	secretValue, err := oldEnvelope.EncryptSecret(oldKey, tenantID, vaultID, "prod/password", "sha256:value", "text/plain", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	resourceVersion, err := store.CurrentResourceVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v2",
		RootKeys:  map[string][]byte{"root-v1": oldRoot, "root-v2": activeRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := services.NewVaultKeyRotationService(store, keyring)
	if err != nil {
		t.Fatal(err)
	}
	result, err := rotation.Rotate(ctx, services.VaultKeyRotationRequest{Limit: 10})
	if err != nil {
		t.Fatalf("rotate vault keys: %v", err)
	}
	if result.Scanned != 1 || result.Rewrapped != 1 || result.Skipped != 0 || result.HasMore {
		t.Fatalf("rotation result = %#v", result)
	}
	rotated, err := store.GetVaultKey(ctx, tenantID, vaultID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RootKeyID != "root-v2" || bytes.Equal(rotated.Salt, oldKey.Salt) || bytes.Equal(rotated.Ciphertext, oldKey.Ciphertext) {
		t.Fatalf("stored key was not rotated: %#v", rotated)
	}
	decrypted, err := keyring.DecryptSecret(rotated, tenantID, vaultID, "prod/password", "sha256:value", "text/plain", secretValue)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("secret became unreadable after rotation: value=%q err=%v", decrypted, err)
	}
	updatedVersion, err := store.CurrentResourceVersion(ctx)
	if err != nil || updatedVersion != resourceVersion {
		t.Fatalf("resource version changed during key rewrap: before=%d after=%d err=%v", resourceVersion, updatedVersion, err)
	}
}

func createRotationScope(ctx context.Context, store *Store, tenantID, vaultID string, now time.Time) error {
	if _, err := store.CreateTenant(ctx, ports.CreateTenantCommand{Tenant: domain.Tenant{
		ID: tenantID, Name: "rotation-tenant", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, ResponseETag: `"tenant-1"`}); err != nil {
		return err
	}
	_, err := store.CreateVault(ctx, ports.CreateVaultCommand{Vault: domain.Vault{
		ID: vaultID, TenantID: tenantID, Name: "rotation-vault", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}, ResponseETag: `"vault-1"`})
	return err
}
