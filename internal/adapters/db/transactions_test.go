package db

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/adapters/encryption"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestRotationBatchAndRetentionRollBackWhenAuditFails(t *testing.T) {
	t.Parallel()
	t.Run("rotation", func(t *testing.T) {
		store := transactionStore(t)
		tenantID, vaultID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
		now := time.Now().UTC()
		if err := createRotationScope(t.Context(), store, tenantID, vaultID, now); err != nil {
			t.Fatal(err)
		}
		otherID := "33333333-3333-4333-8333-333333333333"
		if _, err := store.CreateVault(t.Context(), ports.CreateVaultCommand{Vault: domain.Vault{ID: otherID, TenantID: tenantID, Name: "other", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}, ResponseETag: `"vault-1"`}); err != nil {
			t.Fatal(err)
		}
		oldRoot, newRoot := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
		old, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v1", RootKey: oldRoot})
		if err != nil {
			t.Fatal(err)
		}
		defer old.Destroy()
		for _, id := range []string{vaultID, otherID} {
			key, err := old.NewVaultKey(tenantID, id, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateVaultKey(t.Context(), tenantID, id, key, nil); err != nil {
				t.Fatal(err)
			}
		}
		active, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v2", RootKeys: map[string][]byte{"root-v1": oldRoot, "root-v2": newRoot}})
		if err != nil {
			t.Fatal(err)
		}
		defer active.Destroy()
		rotation, err := services.NewVaultKeyRotationService(store, active)
		if err != nil {
			t.Fatal(err)
		}
		audit, err := services.NewAuditService(store)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.database.ExecContext(t.Context(), `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'no audit'); END`); err != nil {
			t.Fatal(err)
		}
		if err := audit.RecordMutation(t.Context(), func(ctx context.Context) (ports.AuditEvent, error) {
			result, err := rotation.Rotate(ctx, services.VaultKeyRotationRequest{Limit: 200})
			if err == nil && result.Rewrapped != 2 {
				return ports.AuditEvent{}, errors.New("batch did not reach both keys")
			}
			return mutationEvent(), err
		}); err == nil {
			t.Fatal("unaudited rotation committed")
		}
		if count, err := store.CountVaultKeysNotUsingRootKey(t.Context(), "root-v2"); err != nil || count != 2 {
			t.Fatalf("rotation batch escaped rollback: %d %v", count, err)
		}
		if err := store.VerifyVaultKeys(t.Context(), old); err != nil {
			t.Fatal("old keys no longer authenticate after rollback")
		}
	})
	t.Run("retention", func(t *testing.T) {
		store := transactionStore(t)
		for range 3 {
			event := mutationEvent()
			event.OccurredAt = time.Now().Add(-3 * time.Hour)
			if _, err := store.AppendAuditEvent(t.Context(), event); err != nil {
				t.Fatal(err)
			}
		}
		retention, err := services.NewAuditRetentionService(store)
		if err != nil {
			t.Fatal(err)
		}
		audit, err := services.NewAuditService(store)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.database.ExecContext(t.Context(), `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT,'no audit'); END`); err != nil {
			t.Fatal(err)
		}
		if err := audit.RecordMutation(t.Context(), func(ctx context.Context) (ports.AuditEvent, error) {
			_, err := retention.Purge(ctx, services.AuditRetentionRequest{Before: time.Now().Add(-2 * time.Hour), Limit: 3})
			return mutationEvent(), err
		}); err == nil {
			t.Fatal("unaudited retention committed")
		}
		events, err := store.ListAuditEvents(t.Context(), ports.ListAuditEventsQuery{PageSize: 10})
		if err != nil || len(events) != 3 {
			t.Fatalf("retention escaped rollback: %d %v", len(events), err)
		}
	})
}

func transactionStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.Context(), filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func mutationEvent() ports.AuditEvent {
	return ports.AuditEvent{OccurredAt: time.Now().UTC(), RequestID: "transaction-test", Method: "POST", Route: "/api/v1/tenants", StatusCode: 201, Outcome: "success"}
}

func transactionTenant() ports.CreateTenantCommand {
	now := time.Now().UTC()
	return ports.CreateTenantCommand{Tenant: domain.Tenant{ID: "11111111-1111-4111-8111-111111111111", Name: "atomic-tenant", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}, ResponseETag: `"tenant-1"`, IdempotencyKey: "atomic-create", PrincipalID: "operator", RequestHash: "hash", IdempotencyExpiresAt: now.Add(time.Hour)}
}

func TestAuditedTransactionRollbackAndDurableCommit(t *testing.T) {
	t.Parallel()
	store := transactionStore(t)
	audit, err := services.NewAuditService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.ExecContext(t.Context(), `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	command := transactionTenant()
	mutate := func(ctx context.Context) (ports.AuditEvent, error) {
		_, err := store.CreateTenant(ctx, command)
		return mutationEvent(), err
	}
	if err := audit.RecordMutation(t.Context(), mutate); err == nil {
		t.Fatal("audit failure accepted")
	}
	for _, table := range []string{"tenants", "idempotency_records", "audit_events"} {
		var count int
		if err := store.database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s escaped rollback: %d %v", table, count, err)
		} // #nosec G202 -- fixed test table names.
	}
	if version, err := store.CurrentResourceVersion(t.Context()); err != nil || version != 0 {
		t.Fatalf("watch escaped rollback: %d %v", version, err)
	}
	if _, err := store.database.ExecContext(t.Context(), "DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err := audit.RecordMutation(t.Context(), mutate); err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err := store.database.QueryRowContext(t.Context(), "PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopen through the real read-only DSN: data and the audit are durable together.
	reopened, err := OpenReadOnly(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if _, err := reopened.GetTenant(t.Context(), command.Tenant.ID); err != nil {
		t.Fatal(err)
	}
	events, err := reopened.ListAuditEvents(t.Context(), ports.ListAuditEventsQuery{PageSize: 10})
	if err != nil || len(events) != 1 || events[0].RequestID != "transaction-test" {
		t.Fatalf("audit not durable with data: %+v %v", events, err)
	}
}

func TestAuditedTransactionCancellationPanicAndNestedFailure(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "panic", "inner-failure"} {
		t.Run(mode, func(t *testing.T) {
			store := transactionStore(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			called := false
			callback := func(ctx context.Context) error {
				if _, err := store.CreateTenant(ctx, transactionTenant()); err != nil {
					return err
				}
				if err := store.AfterCommit(ctx, func(context.Context) error { called = true; return nil }); err != nil {
					return err
				}
				switch mode {
				case "cancel":
					cancel()
					return nil
				case "panic":
					panic("interrupted handler")
				default:
					// A duplicate tenant fails inside its savepoint. The outer
					// transaction can still read its first successful insert.
					if _, err := store.CreateTenant(ctx, transactionTenant()); err == nil {
						return errors.New("duplicate unexpectedly accepted")
					}
					if _, err := store.GetTenant(ctx, transactionTenant().Tenant.ID); err != nil {
						return err
					}
					return errors.New("reject outer request")
				}
			}
			func() {
				defer func() {
					if value := recover(); value != nil && mode != "panic" {
						t.Fatalf("unexpected panic: %v", value)
					}
				}()
				if err := store.WithinTransaction(ctx, callback); err == nil {
					t.Fatal("failed transaction committed")
				}
			}()
			if called {
				t.Fatal("notification escaped rollback")
			}
			if _, err := store.GetTenant(t.Context(), transactionTenant().Tenant.ID); !errors.Is(err, ports.ErrTenantNotFound) {
				t.Fatalf("mutation escaped %s: %v", mode, err)
			}
		})
	}
}

func TestAuthorizationNotifiesOnlyAfterAuditedCommit(t *testing.T) {
	t.Parallel()
	store := transactionStore(t)
	audit, err := services.NewAuditService(store)
	if err != nil {
		t.Fatal(err)
	}
	bus := authz.NewMemoryPolicyChangesBus()
	defer bus.Close()
	signals, err := bus.Subscribe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policies, err := services.NewAuthorizationPolicyService(store, bus)
	if err != nil {
		t.Fatal(err)
	}
	mutate := func(ctx context.Context) (ports.AuditEvent, error) {
		if err := policies.ReplaceAuthorization(ctx, ports.AuthorizationState{}); err != nil {
			return ports.AuditEvent{}, err
		}
		select {
		case <-signals:
			return ports.AuditEvent{}, errors.New("policy published before audited commit")
		default:
		}
		return mutationEvent(), nil
	}
	if _, err := store.database.ExecContext(t.Context(), `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'no audit'); END`); err != nil {
		t.Fatal(err)
	}
	if err := audit.RecordMutation(t.Context(), mutate); err == nil {
		t.Fatal("unaudited policy committed")
	}
	state, err := store.LoadAuthorization(t.Context())
	if err != nil || state.Initialized || state.Revision != 0 {
		t.Fatalf("policy escaped rollback: %+v %v", state, err)
	}
	select {
	case <-signals:
		t.Fatal("rolled back policy was published")
	default:
	}
	if _, err := store.database.ExecContext(t.Context(), "DROP TRIGGER reject_audit"); err != nil {
		t.Fatal(err)
	}
	if err := audit.RecordMutation(t.Context(), mutate); err != nil {
		t.Fatal(err)
	}
	select {
	case <-signals:
	default:
		t.Fatal("committed policy was not published")
	}
	bus.Close()
	state, err = store.LoadAuthorization(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = audit.RecordMutation(t.Context(), func(ctx context.Context) (ports.AuditEvent, error) {
		return mutationEvent(), policies.ReplaceAuthorization(ctx, state)
	})
	var committed *ports.PostCommitError
	if !errors.As(err, &committed) {
		t.Fatalf("notification failure did not distinguish a committed transaction: %v", err)
	}
	state, err = store.LoadAuthorization(t.Context())
	if err != nil || state.Revision != 2 {
		t.Fatalf("committed policy was lost: %+v %v", state, err)
	}
}

func TestAuditedTransactionRejectsDifferentStores(t *testing.T) {
	t.Parallel()
	first, second := transactionStore(t), transactionStore(t)
	if err := first.WithinTransaction(t.Context(), func(ctx context.Context) error {
		_, err := second.CreateTenant(ctx, transactionTenant())
		return err
	}); err == nil {
		t.Fatal("mixed stores accepted")
	}
	// Cover a formerly autocommit operation as well as BeginTx-based writes.
	command := transactionTenant()
	if _, err := second.CreateTenant(t.Context(), command); err != nil {
		t.Fatal(err)
	}
	if err := first.WithinTransaction(t.Context(), func(ctx context.Context) error {
		_, err := second.UpdateTenant(ctx, command.Tenant.ID, domain.TenantUpdate{Name: "escaped", Labels: map[string]string{}}, nil, time.Now())
		return err
	}); err == nil {
		t.Fatal("foreign autocommit write accepted")
	}
	tenant, err := second.GetTenant(t.Context(), command.Tenant.ID)
	if err != nil || tenant.Name != "atomic-tenant" {
		t.Fatal("foreign write escaped transaction")
	}
	var synchronous int
	if err := first.database.QueryRowContext(t.Context(), "PRAGMA synchronous").Scan(&synchronous); err != nil || synchronous != 2 {
		t.Fatalf("FULL durability missing: %d %v", synchronous, err)
	}
}
