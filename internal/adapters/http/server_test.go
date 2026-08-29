package httpapi_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/adapters/db"
	"github.com/araihu/moco/internal/adapters/encryption"
	httpapi "github.com/araihu/moco/internal/adapters/http"
	internalapi "github.com/araihu/moco/internal/adapters/http/internalapi"
	"github.com/araihu/moco/internal/core/services"
)

const testBearerToken = "test-bearer-token-with-32-bytes-minimum"

type fixture struct {
	server *httptest.Server
	token  string
}

type testResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func TestTenantLifecycleEndToEnd(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})

	response := test.request(t, http.MethodGet, "/livez", nil, nil, false)
	assertStatus(t, response, http.StatusOK)

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
	if !slices.Equal(info.Capabilities, []string{"tenants", "vaults", "secrets", "conditional-writes", "resource-watch"}) {
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

	response = test.request(t, http.MethodGet, alphaLocation, nil, map[string]string{"If-None-Match": alphaETag}, true)
	assertStatus(t, response, http.StatusNotModified)
	if len(response.Body) != 0 {
		t.Fatalf("304 returned a body: %q", response.Body)
	}

	updateBody := []byte(`{"name":"alpha-updated","description":null,"labels":{"owner":"runtime"}}`)
	response = test.request(t, http.MethodPut, alphaLocation, []byte(`{"name":"missing-description","labels":{}}`), nil, true)
	assertStatus(t, response, http.StatusBadRequest)
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

	response = test.request(t, http.MethodDelete, alphaLocation, nil, map[string]string{"If-Match": alphaETag}, true)
	assertStatus(t, response, http.StatusPreconditionFailed)
	response = test.request(t, http.MethodDelete, alphaLocation, nil, map[string]string{"If-Match": updatedETag}, true)
	assertStatus(t, response, http.StatusNoContent)
	response = test.request(t, http.MethodGet, alphaLocation, nil, nil, true)
	assertStatus(t, response, http.StatusNotFound)
}

func TestWatchChangesReturnsCheckpointAndDetectsMutations(t *testing.T) {
	t.Parallel()
	test := newFixture(t, services.TenantServiceOptions{
		CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes"),
	})

	response := test.request(t, http.MethodGet, "/api/v1/watch", nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	initial := decode[httpapi.WatchResult](t, response)
	if initial.ResourceVersion != "rv-0" || initial.Changed || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected initial watch response: %#v, headers %v", initial, response.Header)
	}

	type watchResult struct {
		response testResponse
		err      error
	}
	result := make(chan watchResult, 1)
	go func() {
		response, err := test.rawRequest(http.MethodGet, "/api/v1/watch?resourceVersion=rv-0&timeoutSeconds=5", nil, nil, true)
		if err != nil {
			result <- watchResult{err: err}
			return
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		result <- watchResult{
			response: testResponse{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: body},
			err:      errors.Join(readErr, closeErr),
		}
	}()
	time.Sleep(150 * time.Millisecond)
	test.createTenant(t, "watch-change")
	changedResult := <-result
	if changedResult.err != nil {
		t.Fatalf("watch request failed: %v", changedResult.err)
	}
	changed := changedResult.response
	assertStatus(t, &changed, http.StatusOK)
	watch := decode[httpapi.WatchResult](t, &changed)
	if !watch.Changed || watch.ResourceVersion == "rv-0" {
		t.Fatalf("watch did not observe mutation: %#v", watch)
	}

	response = test.request(t, http.MethodGet, "/api/v1/watch?resourceVersion="+watch.ResourceVersion+"&timeoutSeconds=0", nil, nil, true)
	assertStatus(t, response, http.StatusOK)
	unchanged := decode[httpapi.WatchResult](t, response)
	if unchanged.Changed || unchanged.ResourceVersion != watch.ResourceVersion {
		t.Fatalf("unexpected unchanged watch response: %#v", unchanged)
	}

	response = test.request(t, http.MethodGet, "/api/v1/watch?resourceVersion=rv-999999&timeoutSeconds=0", nil, nil, true)
	assertStatus(t, response, http.StatusBadRequest)
	problem := decode[httpapi.Problem](t, response)
	if problem.Code != "resource_version_ahead" {
		t.Fatalf("unexpected ahead checkpoint problem: %#v", problem)
	}
}

func TestAuthorizationAdministrationIsProtectedAndAtomic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := db.Open(ctx, filepath.Join(t.TempDir(), "moco.db"))
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tenantService, err := services.NewTenantService(store, services.TenantServiceOptions{CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes")})
	if err != nil {
		t.Fatal(err)
	}
	vaultService, err := services.NewVaultService(store, services.VaultServiceOptions{CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes")})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "test-root-v1", RootKey: bytes.Repeat([]byte{0x42}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(envelope.Destroy)
	secretService, err := services.NewSecretService(store, envelope, services.SecretServiceOptions{CursorHMACKey: []byte("test-cursor-key-with-at-least-32-bytes")})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("authorization-admin-token-with-32-bytes"))
	authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{PrincipalID: "admin", TokenSHA256: hex.EncodeToString(digest[:])}})
	if err != nil {
		t.Fatal(err)
	}
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{
		{Subject: "admin", Domain: "*", Path: "/internal/v1/authorization", Method: "GET"},
		{Subject: "admin", Domain: "*", Path: "/internal/v1/authorization", Method: "PUT"},
	})
	if err != nil {
		t.Fatal(err)
	}
	bus := authz.NewMemoryPolicyChangesBus()
	t.Cleanup(bus.Close)
	authorizationService, err := services.NewAuthorizationPolicyService(store, bus)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants: tenantService, Vaults: vaultService, Secrets: secretService,
		Readiness: store, ResourceVersion: store, Authenticator: authenticator, Authorizer: authorizer,
		Authorization: authorizationService, PrincipalCheck: authenticator.HasPrincipal,
		ServiceVersion: "test", Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method string, body []byte, authenticated bool, headerValues ...map[string]string) *httptest.ResponseRecorder {
		request := httptest.NewRequestWithContext(ctx, method, "/internal/v1/authorization", bytes.NewReader(body))
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		if authenticated {
			request.Header.Set("Authorization", "Bearer authorization-admin-token-with-32-bytes")
		}
		if len(headerValues) > 0 {
			for name, value := range headerValues[0] {
				request.Header.Set(name, value)
			}
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	if response := request(http.MethodGet, nil, false); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin read status = %d, want 401", response.Code)
	}
	response := request(http.MethodGet, nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("initial admin read status = %d, body=%s", response.Code, response.Body.String())
	}
	initial := decode[internalapi.AuthorizationSnapshot](t, &testResponse{StatusCode: response.Code, Header: response.Header(), Body: response.Body.Bytes()})
	initialETag := response.Header().Get("ETag")
	if initial.Initialized || initial.Revision != 0 || len(initial.RoleBindings) != 0 || len(initial.Policies) != 0 || initialETag != `"authorization-0"` {
		t.Fatalf("unexpected initial authorization snapshot: %#v", initial)
	}

	valid := []byte(`{"roleBindings":[{"principal":"admin","role":"authorization-admin","domain":"*"}],"policies":[{"subject":"authorization-admin","domain":"*","path":"/internal/v1/authorization","method":"GET"},{"subject":"authorization-admin","domain":"*","path":"/internal/v1/authorization","method":"PUT"}]}`)
	response = request(http.MethodPut, valid, true, map[string]string{"If-Match": initialETag})
	if response.Code != http.StatusOK {
		t.Fatalf("admin replacement status = %d, body=%s", response.Code, response.Body.String())
	}
	replaced := decode[internalapi.AuthorizationSnapshot](t, &testResponse{StatusCode: response.Code, Header: response.Header(), Body: response.Body.Bytes()})
	replacedETag := response.Header().Get("ETag")
	if !replaced.Initialized || replaced.Revision != 1 || len(replaced.RoleBindings) != 1 || len(replaced.Policies) != 2 || replacedETag == initialETag {
		t.Fatalf("unexpected replaced snapshot: %#v", replaced)
	}
	stale := request(http.MethodPut, valid, true, map[string]string{"If-Match": initialETag})
	if stale.Code != http.StatusPreconditionFailed || stale.Header().Get("ETag") != replacedETag {
		t.Fatalf("stale admin replacement status=%d etag=%q, want 412 etag=%q", stale.Code, stale.Header().Get("ETag"), replacedETag)
	}

	invalid := []byte(`{"roleBindings":[{"principal":"admin","role":"authorization-admin","domain":"*"}],"policies":[{"subject":"authorization-admin","domain":"*","path":"/api/v1/invalid?query","method":"GET"}]}`)
	response = request(http.MethodPut, invalid, true, map[string]string{"If-Match": replacedETag})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid admin replacement status = %d, body=%s", response.Code, response.Body.String())
	}
	response = request(http.MethodGet, nil, true)
	if response.Code != http.StatusOK {
		t.Fatalf("post-failure admin read status = %d, body=%s", response.Code, response.Body.String())
	}
	unchanged := decode[internalapi.AuthorizationSnapshot](t, &testResponse{StatusCode: response.Code, Header: response.Header(), Body: response.Body.Bytes()})
	if len(unchanged.RoleBindings) != 1 || len(unchanged.Policies) != 2 {
		t.Fatalf("invalid replacement changed persisted snapshot: %#v", unchanged)
	}
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
			var tenant httpapi.Tenant
			decodeErr := json.NewDecoder(response.Body).Decode(&tenant)
			closeErr := response.Body.Close()
			err = errors.Join(decodeErr, closeErr)
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

	statuses := make(chan int, 2)
	var wait sync.WaitGroup
	for _, name := range []string{"race-one", "race-two"} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			body, _ := json.Marshal(map[string]any{"name": name, "description": nil, "labels": map[string]string{}})
			result := test.request(t, http.MethodPut, path, body, map[string]string{"If-Match": etag}, true)
			statuses <- result.StatusCode
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
	vaultService, err := services.NewVaultService(store, services.VaultServiceOptions{
		CursorHMACKey: options.CursorHMACKey,
		Clock:         options.Clock,
	})
	if err != nil {
		t.Fatalf("create vault service: %v", err)
	}
	envelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "test-root-v1", RootKey: bytes.Repeat([]byte{0x42}, 32),
	})
	if err != nil {
		t.Fatalf("create secret cipher: %v", err)
	}
	secretService, err := services.NewSecretService(store, envelope, services.SecretServiceOptions{
		CursorHMACKey: options.CursorHMACKey,
		Clock:         options.Clock,
	})
	if err != nil {
		t.Fatalf("create secret service: %v", err)
	}
	digest := sha256.Sum256([]byte(testBearerToken))
	authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{
		PrincipalID: "fixture-principal", TokenSHA256: hex.EncodeToString(digest[:]),
	}})
	if err != nil {
		t.Fatalf("create test authenticator: %v", err)
	}
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{
		{Subject: "fixture-principal", Domain: "*", Path: "/api/v1", Method: "GET"},
		{Subject: "fixture-principal", Domain: "*", Path: "/api/v1/*", Method: "*"},
	})
	if err != nil {
		t.Fatalf("create test authorizer: %v", err)
	}
	handler, err := httpapi.NewHandler(httpapi.HandlerOptions{
		Tenants: tenantService, Vaults: vaultService, Secrets: secretService,
		Readiness: store, ResourceVersion: store, Authenticator: authenticator, Authorizer: authorizer,
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
	tenant := decode[httpapi.Tenant](t, response)
	return tenant
}

func (f fixture) request(t *testing.T, method, path string, body []byte, headers map[string]string, authenticated bool) *testResponse {
	t.Helper()
	response, err := f.rawRequest(method, path, body, headers, authenticated)
	if err != nil {
		t.Fatalf("perform request: %v", err)
	}
	payload, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		t.Fatalf("consume response: %v", err)
	}
	return &testResponse{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       payload,
	}
}

func (f fixture) rawRequest(method, path string, body []byte, headers map[string]string, authenticated bool) (*http.Response, error) {
	request, err := http.NewRequestWithContext(context.Background(), method, f.server.URL+path, bytes.NewReader(body))
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

func assertStatus(t *testing.T, response *testResponse, expected int) {
	t.Helper()
	if response.StatusCode == expected {
		return
	}
	t.Fatalf("expected HTTP %d, got %d: %s", expected, response.StatusCode, response.Body)
}

func decode[T any](t *testing.T, response *testResponse) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body, &value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return value
}
