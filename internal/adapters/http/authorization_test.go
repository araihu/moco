package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/araihu/moco/internal/core/ports"
)

func TestAuthorizationResourceRecognizesOnlyContractRoutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		method   string
		path     string
		resource string
		matches  bool
	}{
		{name: "service", method: http.MethodGet, path: "/api/v1", resource: "/api/v1", matches: true},
		{name: "service HEAD", method: http.MethodHead, path: "/api/v1", resource: "/api/v1", matches: true},
		{name: "tenant item", method: http.MethodGet, path: "/api/v1/tenants/t", resource: "/api/v1/tenants/t", matches: true},
		{name: "vault collection", method: http.MethodPost, path: "/api/v1/tenants/t/vaults", resource: "/api/v1/tenants/t/vaults", matches: true},
		{name: "secret item", method: http.MethodGet, path: "/api/v1/tenants/t/vaults/v/secret", resource: "/api/v1/tenants/t/vaults/v/secret", matches: true},
		{name: "metadata", method: http.MethodGet, path: "/api/v1/tenants/t/vaults/v/secret/metadata", resource: "/api/v1/tenants/t/vaults/v/secret/metadata", matches: true},
		{name: "unsupported method", method: http.MethodPatch, path: "/api/v1/tenants/t", matches: false},
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/unknown", matches: false},
		{name: "empty tenant", method: http.MethodGet, path: "/api/v1/tenants//vaults", matches: false},
		{name: "trailing slash", method: http.MethodGet, path: "/api/v1/tenants/t/", resource: "", matches: false},
		{name: "authorization admin read", method: http.MethodGet, path: "/internal/v1/authorization", resource: "/internal/v1/authorization", matches: true},
		{name: "authorization admin replace", method: http.MethodPut, path: "/internal/v1/authorization", resource: "/internal/v1/authorization", matches: true},
		{name: "authorization admin unsupported", method: http.MethodPatch, path: "/internal/v1/authorization", matches: false},
		{name: "health", method: http.MethodGet, path: "/readyz", matches: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), test.method, test.path, nil)
			domain, resource, matches := authorizationResource(request)
			wantDomain := "*"
			if test.name == "tenant item" || test.name == "vault collection" || test.name == "secret item" || test.name == "metadata" {
				wantDomain = "t"
			}
			if matches != test.matches || (matches && (resource != test.resource || domain != wantDomain)) {
				t.Fatalf("authorization target = %q, %q, %t; want %q, %q, %t", domain, resource, matches, wantDomain, test.resource, test.matches)
			}
		})
	}
}

func TestAuthorizationMiddlewareReturnsForbiddenAndPreservesRequestID(t *testing.T) {
	t.Parallel()
	authorizer := recordingAuthorizer{allowed: false}
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := authorizationMiddleware(&authorizer, slog.New(slog.NewTextHandler(io.Discard, nil)), next)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1", nil)
	request = request.WithContext(context.WithValue(request.Context(), requestIDContextKey, "request-123"))
	request = request.WithContext(context.WithValue(request.Context(), principalContextKey, "principal-a"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
	if recorder.Header().Get("X-Request-ID") != "request-123" {
		t.Fatalf("request ID header = %q", recorder.Header().Get("X-Request-ID"))
	}
	if authorizer.principal != "principal-a" || authorizer.domain != "*" || authorizer.resource != "/api/v1" || authorizer.action != http.MethodGet {
		t.Fatalf("unexpected authorization request: %#v", authorizer)
	}
	authorizer.allowed = true
	headRequest := httptest.NewRequestWithContext(t.Context(), http.MethodHead, "/api/v1", nil)
	headRequest = headRequest.WithContext(context.WithValue(headRequest.Context(), requestIDContextKey, "request-124"))
	headRequest = headRequest.WithContext(context.WithValue(headRequest.Context(), principalContextKey, "principal-a"))
	headRecorder := httptest.NewRecorder()
	handler.ServeHTTP(headRecorder, headRequest)
	if headRecorder.Code != http.StatusNoContent || authorizer.domain != "*" || authorizer.action != http.MethodGet {
		t.Fatalf("HEAD was not evaluated as GET: status=%d action=%q", headRecorder.Code, authorizer.action)
	}
}

func TestAuthorizationMiddlewareBypassesInternalAndUnknownRoutes(t *testing.T) {
	t.Parallel()
	authorizer := recordingAuthorizer{allowed: false}
	called := false
	next := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		called = true
		writer.WriteHeader(http.StatusNoContent)
	})
	handler := authorizationMiddleware(&authorizer, slog.Default(), next)
	for _, path := range []string{"/readyz", "/api/v1/unknown"} {
		called = false
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		request = request.WithContext(context.WithValue(request.Context(), principalContextKey, "principal-a"))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if !called || recorder.Code != http.StatusNoContent {
			t.Fatalf("path %s was not passed through: called=%t status=%d", path, called, recorder.Code)
		}
	}
	if authorizer.calls != 0 {
		t.Fatalf("authorizer called %d times for bypassed routes", authorizer.calls)
	}
}

type recordingAuthorizer struct {
	allowed   bool
	principal string
	domain    string
	resource  string
	action    string
	calls     int
}

var _ ports.Authorizer = (*recordingAuthorizer)(nil)

func (authorizer *recordingAuthorizer) Authorize(_ context.Context, principal, domain, resource, action string) (bool, error) {
	authorizer.calls++
	authorizer.principal = principal
	authorizer.domain = domain
	authorizer.resource = resource
	authorizer.action = action
	return authorizer.allowed, nil
}
