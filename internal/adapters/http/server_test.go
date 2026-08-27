package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/db"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	"github.com/araihu/moco/internal/core/services"
)

const testBearerToken = "test-bearer-token-with-32-bytes-minimum"

type fixture struct {
	server *httptest.Server
	token  string
}

func TestTenantLifecycleEndToEnd(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})

	response := test.request(t, http.MethodGet, "/livez", nil, nil, false)
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()

	response = test.request(t, http.MethodGet, "/api/v1", nil, nil, false)
	assertStatus(t, response, http.StatusUnauthorized)
	unauthorized := decode[httpapi.Problem](t, response)
	if unauthorized.Code != "unauthorized" || response.Header.Get("X-Request-ID") == "" || response.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("unexpected unauthorized response: %#v", unauthorized)
	}

	response = test.request(t, http.MethodGet, "/api/v1", nil, map[string]string{
		"Authorization": "bearer " + test.token,
	}, false)
	assertStatus(t, response, http.StatusOK)
	info := decode[httpapi.ServiceInfo](t, response)
	if !slices.Equal(info.Capabilities, []string{"tenants", "conditional-writes"}) {
		t.Fatalf("unexpected capabilities: %v", info.Capabilities)
	}

	response = test.request(t, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"alpha","unknown":true}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	malformed := decode[httpapi.Problem](t, response)
	if malformed.Code != "invalid_json" {
		t.Fatalf("unexpected malformed request problem: %#v", malformed)
	}
	response = test.request(t, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"null-labels","labels":null}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response.Body.Close()

	alphaBody := []byte(`{"name":"alpha","description":"first tenant","externalId":"kubernetes://cluster-a/alpha","labels":{"owner":"platform"}}`)
	createHeaders := map[string]string{"Idempotency-Key": "tenant-alpha-key", "X-Request-ID": "create-alpha"}
	response = test.request(t, http.MethodPost, "/api/v1/tenants", alphaBody, createHeaders, true)
	assertStatus(t, response, http.StatusCreated)
	alphaETag := response.Header.Get("ETag")
	alphaLocation := response.Header.Get("Location")
	if alphaETag == "" || response.Header.Get("X-Request-ID") != "create-alpha" {
		t.Fatalf("missing creation headers: %v", response.Header)
	}
	alpha := decode[httpapi.Tenant](t, response)
	if alpha.Name != "alpha" || alpha.Revision != 1 || alphaLocation != "/api/v1/tenants/"+alpha.Id.String() {
		t.Fatalf("unexpected created tenant: %#v, location %q", alpha, alphaLocation)
	}

	response = test.request(t, http.MethodPost, "/api/v1/tenants", alphaBody, createHeaders, true)
	assertStatus(t, response, http.StatusCreated)
	replay := decode[httpapi.Tenant](t, response)
	if replay.Id != alpha.Id || response.Header.Get("ETag") != alphaETag {
		t.Fatalf("idempotent replay changed response: %#v", replay)
	}

	response = test.request(t, http.MethodPost, "/api/v1/tenants", []byte(`{"name":"different"}`), createHeaders, true)
	assertStatus(t, response, http.StatusConflict)
	idempotencyConflict := decode[httpapi.Problem](t, response)
	if idempotencyConflict.Code != "idempotency_key_conflict" {
		t.Fatalf("unexpected idempotency conflict: %#v", idempotencyConflict)
	}

	beta := test.createTenant(t, "beta")

	response = test.request(t, http.MethodGet, "/api/v1/tenants?limit=1", nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	firstPage := decode[httpapi.TenantList](t, response)
	if len(firstPage.Items) != 1 || !firstPage.Page.HasMore || firstPage.Page.NextCursor == nil {
		t.Fatalf("unexpected first page: %#v", firstPage)
	}
	tamperedCursor := *firstPage.Page.NextCursor + "x"
	response = test.request(t, http.MethodGet, "/api/v1/tenants?cursor="+tamperedCursor, nil, nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response.Body.Close()
	test.createTenant(t, "gamma")
	response = test.request(t, http.MethodGet, "/api/v1/tenants?limit=200&cursor="+*firstPage.Page.NextCursor, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	secondPage := decode[httpapi.TenantList](t, response)
	if len(secondPage.Items) != 1 || secondPage.Items[0].Id != beta.Id || secondPage.Page.HasMore {
		t.Fatalf("snapshot admitted a later insert or lost beta: %#v", secondPage)
	}

	response = test.request(t, http.MethodGet, alphaLocation, nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	if response.Header.Get("ETag") != alphaETag {
		t.Fatalf("GET ETag mismatch: %q", response.Header.Get("ETag"))
	}
	response.Body.Close()

	response = test.request(t, http.MethodGet, alphaLocation, nil, map[string]string{"If-None-Match": alphaETag}, true)
	assertStatus(t, response, http.StatusNotModified)
	if body, _ := io.ReadAll(response.Body); len(body) != 0 {
		t.Fatalf("304 returned a body: %q", body)
	}
	response.Body.Close()

	updateBody := []byte(`{"name":"alpha-updated","description":null,"labels":{"owner":"runtime"}}`)
	response = test.request(t, http.MethodPut, alphaLocation, []byte(`{"name":"missing-description","labels":{}}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	response.Body.Close()
	response = test.request(t, http.MethodPut, alphaLocation, updateBody, map[string]string{"If-Match": `"stale"`}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	stale := decode[httpapi.Problem](t, response)
	if stale.Code != "etag_mismatch" || response.Header.Get("ETag") != alphaETag {
		t.Fatalf("unexpected stale ETag response: %#v, headers %v", stale, response.Header)
	}

	response = test.request(t, http.MethodPut, alphaLocation, updateBody, map[string]string{"If-Match": alphaETag}, true)
	assertStatus(t, response, http.StatusOK)
	updatedETag := response.Header.Get("ETag")
	updated := decode[httpapi.Tenant](t, response)
	if updated.Revision != 2 || updated.Name != "alpha-updated" || updatedETag == alphaETag {
		t.Fatalf("unexpected updated tenant: %#v", updated)
	}

	conflictingBody := []byte(`{"name":"beta","description":null,"labels":{}}`)
	response = test.request(t, http.MethodPut, alphaLocation, conflictingBody, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusConflict)
	conflict := decode[httpapi.Problem](t, response)
	if conflict.Code != "resource_conflict" || conflict.ResourceId == nil || *conflict.ResourceId != beta.Id.String() {
		t.Fatalf("unexpected uniqueness conflict: %#v", conflict)
	}

	vaultPath := alphaLocation + "/vaults"
	response = test.request(t, http.MethodGet, vaultPath, nil, nil, true)
	assertStatus(t, response, http.StatusServiceUnavailable)
	unavailable := decode[httpapi.Problem](t, response)
	if unavailable.Code != "capability_unavailable" || response.Header.Get("Retry-After") != "60" {
		t.Fatalf("unexpected deferred capability response: %#v", unavailable)
	}

	response = test.request(t, http.MethodDelete, alphaLocation, nil, map[string]string{"If-Match": alphaETag}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response.Body.Close()
	response = test.request(t, http.MethodDelete, alphaLocation, nil, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusNoContent)
	response.Body.Close()
	response = test.request(t, http.MethodGet, alphaLocation, nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
	response.Body.Close()
}

func TestConcurrentIdempotentCreateReturnsOneResult(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	body := []byte(`{"name":"idempotent-race","labels":{}}`)
	type result struct {
		status int
		tenant httpapi.Tenant
		err    error
	}
	results := make(chan result, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := test.rawRequest(http.MethodPost, "/api/v1/tenants", body, map[string]string{
				"Idempotency-Key": "concurrent-create-key",
			}, true)
			if err != nil {
				results <- result{err: err}
				return
			}
			defer response.Body.Close()
			var tenant httpapi.Tenant
			err = json.NewDecoder(response.Body).Decode(&tenant)
			results <- result{status: response.StatusCode, tenant: tenant, err: err}
		}()
	}
	wait.Wait()
	close(results)
	created := []result{}
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent create failed: %v", item.err)
		}
		created = append(created, item)
	}
	if created[0].status != http.StatusCreated || created[1].status != http.StatusCreated || created[0].tenant.Id != created[1].tenant.Id {
		t.Fatalf("idempotent creates diverged: %#v", created)
	}
	response := test.request(t, http.MethodGet, "/api/v1/tenants?name=idempotent-race", nil, nil, true)
	page := decode[httpapi.TenantList](t, response)
	if len(page.Items) != 1 {
		t.Fatalf("expected one persisted tenant, got %#v", page.Items)
	}
}

func TestConcurrentConditionalUpdateAllowsOneWinner(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})
	tenant := test.createTenant(t, "race")
	path := "/api/v1/tenants/" + tenant.Id.String()
	response := test.request(t, http.MethodGet, path, nil, nil, true)
	etag := response.Header.Get("ETag")
	response.Body.Close()

	statuses := make(chan int, 2)
	var wait sync.WaitGroup
	for _, name := range []string{"race-one", "race-two"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, _ := json.Marshal(map[string]any{"name": name, "description": nil, "labels": map[string]string{}})
			result := test.request(t, http.MethodPut, path, body, map[string]string{"If-Match": etag}, true)
			statuses <- result.StatusCode
			result.Body.Close()
		}()
	}
	wait.Wait()
	close(statuses)
	got := []int{}
	for status := range statuses {
		got = append(got, status)
	}
	slices.Sort(got)
	if !slices.Equal(got, []int{http.StatusOK, http.StatusPreconditionFailed}) {
		t.Fatalf("expected one update winner, got statuses %v", got)
	}
}

func TestTenantCursorExpires(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
		Clock:         func() time.Time { return now },
	})
	test.createTenant(t, "one")
	test.createTenant(t, "two")
	response := test.request(t, http.MethodGet, "/api/v1/tenants?limit=1", nil, nil, true)
	page := decode[httpapi.TenantList](t, response)
	if page.Page.NextCursor == nil {
		t.Fatal("expected a continuation cursor")
	}
	now = now.Add(16 * time.Minute)
	response = test.request(t, http.MethodGet, "/api/v1/tenants?limit=1&cursor="+*page.Page.NextCursor, nil, nil, true)
	assertStatus(t, response, http.StatusGone)
	problem := decode[httpapi.Problem](t, response)
	if problem.Code != "cursor_expired" {
		t.Fatalf("unexpected cursor problem: %#v", problem)
	}
}

func newFixture(t *testing.T, options services.TenantServiceOptions) fixture {
	t.Helper()
	store, err := db.Open(context.Background(), filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tenantService, err := services.NewTenantService(store, options)
	if err != nil {
		t.Fatalf("create tenant service: %v", err)
	}
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants: tenantService, Readiness: store, BearerToken: testBearerToken,
		ServiceVersion: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("create HTTP handler: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return fixture{server: server, token: testBearerToken}
}

func (f fixture) createTenant(t *testing.T, name string) httpapi.Tenant {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "labels": map[string]string{}})
	response := f.request(t, http.MethodPost, "/api/v1/tenants", body, nil, true)
	assertStatus(t, response, http.StatusCreated)
	return decode[httpapi.Tenant](t, response)
}

func (f fixture) request(t *testing.T, method, path string, body []byte, headers map[string]string, authenticated bool) *http.Response {
	t.Helper()
	response, err := f.rawRequest(method, path, body, headers, authenticated)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	return response
}

func (f fixture) rawRequest(method, path string, body []byte, headers map[string]string, authenticated bool) (*http.Response, error) {
	request, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if authenticated {
		request.Header.Set("Authorization", "Bearer "+f.token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return f.server.Client().Do(request)
}

func assertStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	if response.StatusCode == expected {
		return
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	t.Fatalf("expected HTTP %d, got %d: %s", expected, response.StatusCode, body)
}

func decode[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	defer response.Body.Close()
	var value T
	if err := json.NewDecoder(response.Body).Decode(&value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return value
}
