package db

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/araihu/moco/internal/core/ports"
)

func TestAuthorizationSnapshotPersistsAcrossRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "moco.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	state := ports.AuthorizationState{
		RoleBindings: []ports.AuthorizationRoleBinding{{Principal: "controller", Role: "reader", Domain: "tenant-a"}},
		Policies:     []ports.AuthorizationPolicy{{Subject: "reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}", Method: "GET"}},
	}
	if err := store.ReplaceAuthorization(ctx, state); err != nil {
		t.Fatalf("replace authorization: %v", err)
	}
	loaded, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatalf("load authorization: %v", err)
	}
	if !reflect.DeepEqual(loaded, state) {
		t.Fatalf("loaded state = %#v, want %#v", loaded, state)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted, err := reopened.LoadAuthorization(ctx)
	if err != nil {
		t.Fatalf("load authorization after restart: %v", err)
	}
	if !reflect.DeepEqual(restarted, state) {
		t.Fatalf("restarted state = %#v, want %#v", restarted, state)
	}
}

func TestAuthorizationSnapshotReplacementRollsBackOnDuplicate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	original := ports.AuthorizationState{
		RoleBindings: []ports.AuthorizationRoleBinding{{Principal: "controller", Role: "reader", Domain: "tenant-a"}},
		Policies:     []ports.AuthorizationPolicy{{Subject: "reader", Domain: "tenant-a", Path: "/api/v1", Method: "GET"}},
	}
	if err := store.ReplaceAuthorization(ctx, original); err != nil {
		t.Fatal(err)
	}
	invalid := original
	invalid.Policies = append(invalid.Policies, invalid.Policies[0])
	if err := store.ReplaceAuthorization(ctx, invalid); err == nil {
		t.Fatal("duplicate policy replacement unexpectedly succeeded")
	}
	loaded, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, original) {
		t.Fatalf("failed replacement changed state: %#v, want %#v", loaded, original)
	}
}
