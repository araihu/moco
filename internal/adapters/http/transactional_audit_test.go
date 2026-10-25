package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func auditSQL(t *testing.T, fixture fixture) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// Snapshot every business table, including private ciphertext, policy, CAS/watch
// checkpoints and idempotency. GET attempt logs may grow independently.
func businessSnapshot(t *testing.T, database *sql.DB) string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT IN ('audit_events','sqlite_sequence') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var snapshot strings.Builder
	for _, table := range tables {
		snapshot.WriteString(table + ":" + snapshotBusinessTable(t, database, table) + "\n")
	}
	return snapshot.String()
}

func snapshotBusinessTable(t *testing.T, database *sql.DB, table string) string {
	t.Helper()
	// Table names originate in the fixed embedded migration schema.
	rows, err := database.QueryContext(t.Context(), `SELECT * FROM "`+strings.ReplaceAll(table, `"`, `""`)+`"`) // #nosec G202 -- schema-controlled identifiers, quoted defensively.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var records []string
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for i := range values {
			destinations[i] = &values[i]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, string(payload))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	sort.Strings(records)
	return strings.Join(records, "\n")
}

func TestAuditInsertAndCommitFailuresRollBackEveryResourceMutation(t *testing.T) {
	t.Parallel()
	for _, fault := range []string{"insert", "commit"} {
		for _, operation := range []string{"create-tenant", "create-vault", "update-tenant", "update-vault", "put-secret", "new-vault-key", "delete-secret", "cascade-vault", "cascade-tenant"} {
			t.Run(fault+"/"+operation, func(t *testing.T) {
				t.Parallel()
				fixture := newFixture(t, services.TenantServiceOptions{CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes")})
				tenant := fixture.createTenant(t, "atomic-parent")
				tenantPath := "/api/v1/tenants/" + tenant.Id.String()
				vault := fixture.createVault(t, tenantPath+"/vaults", "populated")
				vaultPath := tenantPath + "/vaults/" + vault.Id.String()
				secretPath := vaultPath + "/secret?path=prod/password"
				fixture.request(t, http.MethodPut, secretPath, []byte(`{"value":"b3JpZ2luYWw="}`), nil, true)
				method, path, body, want := http.MethodPost, "/api/v1/tenants", `{"name":"retry-tenant"}`, 201
				headers := map[string]string{"X-Request-ID": "audit-fault", "Idempotency-Key": "reuse-after-rollback"}
				switch operation {
				case "create-vault":
					path = tenantPath + "/vaults"
					body = `{"name":"retry-vault"}`
				case "update-tenant":
					method, path, body, want = http.MethodPut, tenantPath, `{"name":"changed-tenant","description":null,"labels":{}}`, 200
				case "update-vault":
					method, path, body, want = http.MethodPut, vaultPath, `{"name":"changed-vault","description":null,"labels":{}}`, 200
				case "put-secret":
					method, path, body, want = http.MethodPut, secretPath, `{"value":"Y2hhbmdlZA=="}`, 200
				case "new-vault-key":
					empty := fixture.createVault(t, tenantPath+"/vaults", "empty")
					method, path, body = http.MethodPut, tenantPath+"/vaults/"+empty.Id.String()+"/secret?path=new/key", `{"value":"bmV3LXZhbHVl"}`
				case "delete-secret":
					method, path, body, want = http.MethodDelete, secretPath, "", 204
				case "cascade-vault":
					method, path, body, want = http.MethodDelete, vaultPath+"?cascade=true", "", 204
				case "cascade-tenant":
					method, path, body, want = http.MethodDelete, tenantPath+"?cascade=true", "", 204
				}
				database := auditSQL(t, fixture)
				if operation == "create-tenant" || operation == "create-vault" {
					seed := fixture.request(t, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"expired-idempotency"}`), map[string]string{"Idempotency-Key": "expired-seed"}, true)
					assertStatus(t, seed, http.StatusCreated)
					if _, err := database.ExecContext(t.Context(), `UPDATE idempotency_records SET expires_at='2000-01-01T00:00:00.000000000Z'`); err != nil {
						t.Fatal(err)
					}
				}
				faultSQL := `CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events WHEN NEW.request_id='audit-fault' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`
				if fault == "commit" {
					faultSQL = `CREATE TABLE deferred_audit_check (tenant_id TEXT REFERENCES tenants(id) DEFERRABLE INITIALLY DEFERRED);
                CREATE TRIGGER reject_audit AFTER INSERT ON audit_events WHEN NEW.request_id='audit-fault' BEGIN INSERT INTO deferred_audit_check VALUES ('missing-tenant'); END`
				}
				if _, err := database.ExecContext(t.Context(), faultSQL); err != nil {
					t.Fatal(err)
				}
				before := businessSnapshot(t, database)
				response := fixture.request(t, method, path, []byte(body), headers, true)
				assertStatus(t, response, http.StatusServiceUnavailable)
				if response.Header.Get("ETag") != "" || response.Header.Get("Location") != "" {
					t.Fatal("uncommitted success headers escaped")
				}
				if after := businessSnapshot(t, database); after != before {
					t.Fatalf("%s escaped audit rollback", operation)
				}
				var count int
				if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_events WHERE request_id='audit-fault'`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("false success audit: %d %v", count, err)
				}
				if _, err := database.ExecContext(t.Context(), "DROP TRIGGER reject_audit"); err != nil {
					t.Fatal(err)
				}
				response = fixture.request(t, method, path, []byte(body), headers, true)
				assertStatus(t, response, want)
				if err := database.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM audit_events WHERE request_id='audit-fault' AND outcome='success' AND status_code=?`, want).Scan(&count); err != nil || count != 1 {
					t.Fatalf("expected one committed audit: %d %v", count, err)
				}
				if operation == "create-tenant" || operation == "create-vault" {
					after := businessSnapshot(t, database)
					headers["X-Request-ID"] = "idempotent-replay"
					replay := fixture.request(t, method, path, []byte(body), headers, true)
					assertStatus(t, replay, want)
					if !bytes.Equal(replay.Body, response.Body) || replay.Header.Get("ETag") != response.Header.Get("ETag") || businessSnapshot(t, database) != after {
						t.Fatal("retry idempotency was lost after rollback")
					}
				}
			})
		}
	}
}

type gatedAudit struct {
	*db.Store
	entered, release chan struct{}
}

func (g *gatedAudit) AppendAuditEvent(ctx context.Context, event ports.AuditEvent) (ports.AuditEvent, error) {
	close(g.entered)
	select {
	case <-g.release:
	case <-ctx.Done():
		return ports.AuditEvent{}, ctx.Err()
	}
	return g.Store.AppendAuditEvent(ctx, event)
}

type trackedResponse struct {
	*httptest.ResponseRecorder
	written chan struct{}
	once    sync.Once
}

func (w *trackedResponse) WriteHeader(status int) {
	w.once.Do(func() { close(w.written) })
	w.ResponseRecorder.WriteHeader(status)
}

func TestSuccessResponseAndDatabaseAreHiddenUntilAuditCommits(t *testing.T) {
	t.Parallel()
	gate := &gatedAudit{entered: make(chan struct{}), release: make(chan struct{})}
	fixture := newFixture(t, services.TenantServiceOptions{CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes")}, func(store *db.Store) ports.AuditRepository { gate.Store = store; return gate })
	defer close(gate.release)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/tenants", bytes.NewBufferString(`{"name":"not-visible-yet"}`))
	request.Header.Set("Authorization", "Bearer "+testBearerToken)
	request.Header.Set("Content-Type", "application/json")
	response := &trackedResponse{ResponseRecorder: httptest.NewRecorder(), written: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); fixture.server.Config.Handler.ServeHTTP(response, request) }()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("audit never reached commit barrier")
	}
	select {
	case <-response.written:
		t.Fatal("success response escaped before audit")
	default:
	}
	database := auditSQL(t, fixture)
	var count int
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM tenants").Scan(&count); err != nil || count != 0 {
		t.Fatal("uncommitted tenant became visible")
	}
	gate.release <- struct{}{}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	if response.Code != 201 {
		t.Fatalf("committed status: %d", response.Code)
	}
	if err := database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM audit_events").Scan(&count); err != nil || count != 1 {
		t.Fatal("missing or duplicate success event")
	}
}
