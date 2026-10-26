package moco

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func fixture(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	c, err := New(s.URL, s.URL, "private-token", ca)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c, s
}

func TestEnsureAdoptionAndConditionalDriftRepair(t *testing.T) {
	external := "kubernetes://lab/platform/a+b &?"
	puts := 0
	current := Resource{ID: "tenant-id", ExternalID: external, Revision: 2, Metadata: Metadata{Name: "old", Labels: map[string]string{}}}
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Error("missing credentials")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/tenants":
			if r.URL.Query().Get("externalId") != external {
				t.Error("externalId encoding changed")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []Resource{current}})
		case r.Method == "GET":
			w.Header().Set("Etag", `"2"`)
			_ = json.NewEncoder(w).Encode(current)
		case r.Method == "PUT":
			puts++
			if r.Header.Get("If-Match") != `"2"` {
				t.Error("missing optimistic concurrency")
			}
			if err := json.NewDecoder(r.Body).Decode(&current.Metadata); err != nil {
				t.Error(err)
			}
			current.Revision = 3
			w.Header().Set("ETag", `"3"`)
			_ = json.NewEncoder(w).Encode(current)
		default:
			t.Error("unexpected mutation")
		}
	})
	for range 2 {
		resource, err := c.Ensure(t.Context(), "/api/v1/tenants", external, Metadata{Name: "desired"})
		if err != nil || resource.ID != "tenant-id" || resource.Revision != 3 {
			t.Fatalf("ensure: %+v %v", resource, err)
		}
	}
	if puts != 1 {
		t.Fatalf("no-op should not write, got %d puts", puts)
	}
}

func TestCreateUsesExternalIDAndConflictDoesNotReplay(t *testing.T) {
	posts := 0
	c, _ := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_, _ = fmt.Fprint(w, `{"items":[]}`)
			return
		}
		posts++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if payload["externalId"] != "external" || payload["labels"] == nil {
			t.Error("incomplete create contract")
		}
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, "private-token should never be surfaced")
	})
	_, err := c.Ensure(t.Context(), "/api/v1/tenants", "external", Metadata{Name: "a"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 409 || posts != 1 {
		t.Fatalf("conflict: %v posts=%d", err, posts)
	}
	if err.Error() != "Moco returned HTTP 409" {
		t.Fatal("response body leaked")
	}
}

func TestRejectInsecureEndpointsUntrustedTLSAndRedirects(t *testing.T) {
	c, s := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://example.invalid", http.StatusFound)
	})
	_, _, err := c.Authorization(context.Background())
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 302 {
		t.Fatalf("redirect must not be followed: %v", err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	for _, endpoint := range []string{"http://moco", "https://user:pass@moco", "https://moco/path", "https://moco?token=x"} {
		if _, err := New(endpoint, s.URL, "token", ca); err == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
	untrusted := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer untrusted.Close()
	// httptest uses a shared fixture certificate; use an unrelated empty CA pool.
	transport := c.http.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.RootCAs = nil
	c.http.Transport = transport
	if _, _, err := c.Authorization(t.Context()); err == nil {
		t.Fatal("untrusted TLS accepted")
	}
}

func TestAuthorizationRejectsWeakOrMissingETag(t *testing.T) {
	for _, etag := range []string{"", `W/"3"`} {
		t.Run(etag, func(t *testing.T) {
			c, _ := fixture(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("ETag", etag)
				_, _ = fmt.Fprint(w, `{"revision":3,"policies":[],"roleBindings":[]}`)
			})
			if _, _, err := c.Authorization(t.Context()); err == nil {
				t.Fatal("unsafe ETag accepted")
			}
		})
	}
}
