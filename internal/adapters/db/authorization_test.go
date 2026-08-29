package db

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
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
	initial, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatalf("load initial authorization: %v", err)
	}
	if initial.Initialized {
		t.Fatalf("new database authorization unexpectedly initialized: %#v", initial)
	}
	if initial.Revision != 0 {
		t.Fatalf("new database authorization revision = %d, want 0", initial.Revision)
	}
	state := ports.AuthorizationState{
		Initialized:  true,
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
	state.Revision = 1
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
		Initialized:  true,
		RoleBindings: []ports.AuthorizationRoleBinding{{Principal: "controller", Role: "reader", Domain: "tenant-a"}},
		Policies:     []ports.AuthorizationPolicy{{Subject: "reader", Domain: "tenant-a", Path: "/api/v1", Method: "GET"}},
	}
	if err := store.ReplaceAuthorization(ctx, original); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	invalid := persisted
	invalid.Policies = append(invalid.Policies, invalid.Policies[0])
	if err := store.ReplaceAuthorization(ctx, invalid); err == nil {
		t.Fatal("duplicate policy replacement unexpectedly succeeded")
	}
	loaded, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, persisted) {
		t.Fatalf("failed replacement changed state: %#v, want %#v", loaded, persisted)
	}
}

func TestAuthorizationSnapshotRejectsStaleRevision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	first := ports.AuthorizationState{
		RoleBindings: []ports.AuthorizationRoleBinding{{Principal: "controller", Role: "reader", Domain: "tenant-a"}},
		Policies:     []ports.AuthorizationPolicy{{Subject: "reader", Domain: "tenant-a", Path: "/api/v1", Method: "GET"}},
	}
	if err := store.ReplaceAuthorization(ctx, first); err != nil {
		t.Fatal(err)
	}
	stale := first
	stale.Policies = []ports.AuthorizationPolicy{{Subject: "reader", Domain: "tenant-a", Path: "/api/v1/other", Method: "GET"}}
	if err := store.ReplaceAuthorization(ctx, stale); !errors.Is(err, ports.ErrAuthorizationRevisionConflict) {
		t.Fatalf("stale replacement error = %v, want revision conflict", err)
	}
	loaded, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Revision != 1 || loaded.Policies[0].Path != "/api/v1" {
		t.Fatalf("stale replacement changed authoritative state: %#v", loaded)
	}
}

func TestAuthorizationSnapshotConcurrentWritersHaveOneWinner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.ReplaceAuthorization(ctx, ports.AuthorizationState{}); err != nil {
		t.Fatal(err)
	}
	base, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	states := []ports.AuthorizationState{
		{Revision: base.Revision, Policies: []ports.AuthorizationPolicy{{Subject: "reader", Domain: "*", Path: "/api/v1/one", Method: "GET"}}},
		{Revision: base.Revision, Policies: []ports.AuthorizationPolicy{{Subject: "reader", Domain: "*", Path: "/api/v1/two", Method: "GET"}}},
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(states))
	var wait sync.WaitGroup
	for _, state := range states {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- store.ReplaceAuthorization(ctx, state)
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	winners, conflicts := 0, 0
	for replacementErr := range errorsCh {
		switch {
		case replacementErr == nil:
			winners++
		case errors.Is(replacementErr, ports.ErrAuthorizationRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent replacement failed unexpectedly: %v", replacementErr)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent replacements = winners %d, conflicts %d; want one each", winners, conflicts)
	}
	latest, err := store.LoadAuthorization(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if latest.Revision != base.Revision+1 || len(latest.Policies) != 1 {
		t.Fatalf("unexpected concurrent winner state: %#v", latest)
	}
}
