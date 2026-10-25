package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestProductionListenerIsolationAndMetricsAuthentication(t *testing.T) {
	t.Parallel()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	public := PublicListener(ok)
	for _, path := range []string{"/internal/v1/audit", "//internal/v1/authorization", "/api/../internal/v1/audit", "/metrics", "/%69nternal/v1/audit"} {
		response := httptest.NewRecorder()
		public.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
		if response.Code != 404 {
			t.Fatalf("public route %s exposed: %d", path, response.Code)
		}
	}
	admin := MetricsAuthentication("metrics-credential-with-at-least-32-bytes", ok, ok)
	for _, token := range []string{"", "Bearer wrong", "Bearer metrics-credential-with-at-least-32-bytes"} {
		request := httptest.NewRequestWithContext(t.Context(), "GET", "/metrics", nil)
		request.Header.Set("Authorization", token)
		response := httptest.NewRecorder()
		admin.ServeHTTP(response, request)
		want := 401
		if strings.HasSuffix(token, "32-bytes") {
			want = 204
		}
		if response.Code != want {
			t.Fatalf("metrics authorization: %d", response.Code)
		}
	}
}

type failedAuditStorage struct{ ports.AuditRepository }

func (failedAuditStorage) AppendAuditEvent(context.Context, ports.AuditEvent) (ports.AuditEvent, error) {
	return ports.AuditEvent{}, errors.New("disk write failed")
}

func TestAuditPersistenceIncidentReachesOperationalHealth(t *testing.T) {
	t.Parallel()
	audit, err := services.NewAuditService(failedAuditStorage{})
	if err != nil {
		t.Fatal(err)
	}
	metrics := NewMetrics()
	handler := requestIDs(auditMiddleware(audit, []byte("audit-test-hmac-key-at-least-32-bytes"), slog.New(slog.NewTextHandler(io.Discard, nil)),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), metrics.AuditResult))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), "GET", "/api/v1/tenants", nil))
	if response.Code != http.StatusNoContent || metrics.AuditHealthy() {
		t.Fatal("post-response audit persistence failure did not reach operational health")
	}
}

func TestMetricsHaveBoundedLabelsAndDetectAuditFailure(t *testing.T) {
	t.Parallel()
	metrics := NewMetrics()
	handler := metrics.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(403) }), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, path := range []string{"/api/v1/tenants/sensitive-tenant/vaults/sensitive-vault/secret?path=private/password", "/api/v1/random-identifier"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), "GET", path, nil))
	}
	metrics.AuditResult(errors.New("storage failed"))
	metrics.AuditResult(nil)
	if metrics.AuditHealthy() {
		t.Fatal("audit failure was silently cleared")
	}
	response := httptest.NewRecorder()
	metrics.ServePrometheus(response, "release-123")
	text := response.Body.String()
	for _, unexpected := range []string{"sensitive-tenant", "sensitive-vault", "password", "random-identifier"} {
		if strings.Contains(text, unexpected) {
			t.Fatal("sensitive or unbounded metric label")
		}
	}
	for _, expected := range []string{`moco_http_requests_total{route="api",method="GET",status="403"} 2`, "moco_audit_failures_total 1", `le="+Inf"} 2`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing metric %q", expected)
		}
	}
}
