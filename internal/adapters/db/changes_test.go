package db

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
)

func TestResourceVersionAdvancesForResourceMutations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	version, err := store.CurrentResourceVersion(ctx)
	if err != nil || version != 0 {
		t.Fatalf("initial resource version = %d, err=%v", version, err)
	}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	tenantID := "11111111-1111-4111-8111-111111111111"
	if _, err := store.CreateTenant(ctx, ports.CreateTenantCommand{Tenant: domain.Tenant{
		ID: tenantID, Name: "revision-tenant", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	version, err = store.CurrentResourceVersion(ctx)
	if err != nil || version != 1 {
		t.Fatalf("after tenant create resource version = %d, err=%v", version, err)
	}
	if _, err := store.CreateVault(ctx, ports.CreateVaultCommand{Vault: domain.Vault{
		ID: "22222222-2222-4222-8222-222222222222", TenantID: tenantID, Name: "revision-vault", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	version, err = store.CurrentResourceVersion(ctx)
	if err != nil || version != 2 {
		t.Fatalf("after vault create resource version = %d, err=%v", version, err)
	}
	if err := store.DeleteVault(ctx, tenantID, "22222222-2222-4222-8222-222222222222", nil, false); err != nil {
		t.Fatal(err)
	}
	version, err = store.CurrentResourceVersion(ctx)
	if err != nil || version != 3 {
		t.Fatalf("after vault delete resource version = %d, err=%v", version, err)
	}
}

func TestTenantResourceVersionScopesLogicalMutationsAndRetainsTombstone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	unknown := "99999999-9999-4999-8999-999999999999"
	if _, err := store.CurrentTenantResourceVersion(ctx, unknown); !errors.Is(err, ports.ErrTenantNotFound) {
		t.Fatalf("unknown tenant checkpoint error = %v, want tenant not found", err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	tenantA := "11111111-1111-4111-8111-111111111111"
	tenantB := "22222222-2222-4222-8222-222222222222"
	for _, tenant := range []struct{ id, name string }{{tenantA, "tenant-a"}, {tenantB, "tenant-b"}} {
		if _, err := store.CreateTenant(ctx, ports.CreateTenantCommand{Tenant: domain.Tenant{
			ID: tenant.id, Name: tenant.name, Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 1 {
		t.Fatalf("tenant A initial revision = %d, err=%v", revision, err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantB); err != nil || revision != 1 {
		t.Fatalf("tenant B initial revision = %d, err=%v", revision, err)
	}
	vaultID := "33333333-3333-4333-8333-333333333333"
	if _, err := store.CreateVault(ctx, ports.CreateVaultCommand{Vault: domain.Vault{
		ID: vaultID, TenantID: tenantA, Name: "vault-a", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 2 {
		t.Fatalf("tenant A after vault revision = %d, err=%v", revision, err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantB); err != nil || revision != 1 {
		t.Fatalf("tenant B changed after tenant A vault mutation = %d, err=%v", revision, err)
	}
	if _, err := store.PutSecret(ctx, ports.PutSecretCommand{
		TenantID: tenantA, VaultID: vaultID, Path: "prod/password",
		Digest: "sha256:0000000000000000000000000000000000000000000000000000000000000000", ContentType: "text/plain",
		Value: ports.EncryptedSecretValue{Salt: bytes.Repeat([]byte{0x01}, 32), Ciphertext: bytes.Repeat([]byte{0x02}, 17)}, UpdatedAt: now, CreateOnly: true,
	}); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 3 {
		t.Fatalf("tenant A after secret create revision = %d, err=%v", revision, err)
	}
	if err := store.DeleteSecret(ctx, ports.DeleteSecretCommand{TenantID: tenantA, VaultID: vaultID, Path: "prod/password"}); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 4 {
		t.Fatalf("tenant A after secret delete revision = %d, err=%v", revision, err)
	}
	if err := store.DeleteVault(ctx, tenantA, vaultID, nil, false); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 5 {
		t.Fatalf("tenant A after vault delete revision = %d, err=%v", revision, err)
	}
	if err := store.DeleteTenant(ctx, tenantA, nil, false); err != nil {
		t.Fatal(err)
	}
	if revision, err := store.CurrentTenantResourceVersion(ctx, tenantA); err != nil || revision != 6 {
		t.Fatalf("tenant A tombstone revision = %d, err=%v", revision, err)
	}
}
