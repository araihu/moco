package httpapi

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestMutationCannotFallBackToMissingOrNontransactionalAudit(t *testing.T) {
	t.Parallel()
	nontransactional, err := services.NewAuditService(failedAuditStorage{})
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range []*services.AuditService{nil, nontransactional} {
		called := false
		handler := requestIDs(transactionalAudit(audit, bytes.Repeat([]byte{1}, 32), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "POST", "/api/v1/tenants", nil))
		if called || response.Code != 503 {
			t.Fatal("unaudited mutation was allowed")
		}
	}
}

func TestMutationResponseEncodingFailureAndCancellationRollBack(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"oversize", "cancel", "encoding-failure"} {
		t.Run(mode, func(t *testing.T) {
			store, err := db.Open(t.Context(), filepath.Join(t.TempDir(), "moco.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			audit, err := services.NewAuditService(store)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id := "11111111-1111-4111-8111-111111111111"
			handler := requestIDs(transactionalAudit(audit, bytes.Repeat([]byte{1}, 32), slog.New(slog.NewTextHandler(io.Discard, nil)), nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				now := time.Now().UTC()
				if _, err := store.CreateTenant(r.Context(), ports.CreateTenantCommand{Tenant: domain.Tenant{ID: id, Name: "rollback", Labels: map[string]string{}, Revision: 1, CreatedAt: now, UpdatedAt: now}, ResponseETag: `"tenant-1"`}); err != nil {
					t.Error(err)
					return
				}
				w.Header().Set("ETag", `"uncommitted"`)
				w.Header().Set("Location", "/uncommitted")
				w.WriteHeader(201)
				switch mode {
				case "oversize":
					_, _ = w.Write(make([]byte, maxMutationResponseBytes+1))
				case "cancel":
					cancel()
				default:
					_, _ = w.Write([]byte("partial-json"))
					w.WriteHeader(500)
				}
			})))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequestWithContext(ctx, "POST", "/api/v1/tenants", nil))
			if response.Code != 503 || response.Header().Get("ETag") != "" || response.Header().Get("Location") != "" {
				t.Fatal("uncommitted response was released")
			}
			if _, err := store.GetTenant(t.Context(), id); !errors.Is(err, ports.ErrTenantNotFound) {
				t.Fatalf("mutation escaped rollback: %v", err)
			}
			events, err := store.ListAuditEvents(t.Context(), ports.ListAuditEventsQuery{PageSize: 10})
			if err != nil || len(events) != 0 {
				t.Fatal("false success audit escaped rollback")
			}
		})
	}
}
