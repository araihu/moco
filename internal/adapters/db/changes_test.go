package db

import (
	"context"
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
