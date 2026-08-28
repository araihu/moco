package authz_test

import (
	"context"
	"testing"

	"github.com/araihu/moco/internal/adapters/authz"
)

func TestStaticAuthorizerUsesRolesAndFailsClosed(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(
		[]authz.RoleBinding{
			{Principal: "operator-a", Role: "secret-reader", Domain: "tenant-a"},
			{Principal: "operator-b", Role: "admin", Domain: "*"},
		},
		[]authz.Policy{
			{Subject: "secret-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET"},
			{Subject: "admin", Domain: "*", Path: "/api/v1", Method: "GET"},
			{Subject: "admin", Domain: "*", Path: "/api/v1/*", Method: "*"},
		},
	)
	if err != nil {
		t.Fatalf("construct authorizer: %v", err)
	}
	ctx := context.Background()
	allowed, err := authorizer.Authorize(ctx, "operator-a", "tenant-a", "/api/v1/tenants/tenant/vaults/vault/secret", "GET")
	if err != nil || !allowed {
		t.Fatalf("reader secret GET: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(ctx, "operator-a", "tenant-a", "/api/v1/tenants/tenant/vaults/vault/secret", "PUT")
	if err != nil || allowed {
		t.Fatalf("reader secret PUT unexpectedly allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(ctx, "operator-a", "tenant-b", "/api/v1/tenants/other/vaults/vault/secret", "GET")
	if err != nil || allowed {
		t.Fatalf("tenant domain isolation failed: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(ctx, "unknown", "tenant-a", "/api/v1/tenants/tenant/vaults/vault/secret", "GET")
	if err != nil || allowed {
		t.Fatalf("unknown principal unexpectedly allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(ctx, "admin", "tenant-b", "/api/v1/tenants/tenant/vaults/vault/secret", "DELETE")
	if err != nil || !allowed {
		t.Fatalf("admin wildcard denied: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(ctx, "operator-b", "tenant-b", "/api/v1/tenants/tenant/vaults/vault/secret", "DELETE")
	if err != nil || !allowed {
		t.Fatalf("global role wildcard denied: allowed=%t, err=%v", allowed, err)
	}
}

func TestStaticAuthorizerRejectsUnsafePolicies(t *testing.T) {
	t.Parallel()
	tests := map[string]authz.Policy{
		"outside API":       {Subject: "reader", Domain: "*", Path: "/other/*", Method: "GET"},
		"regex literal":     {Subject: "reader", Domain: "*", Path: "/api/v1/tenants/{tenantId}.*/vaults", Method: "GET"},
		"wildcard not last": {Subject: "reader", Domain: "*", Path: "/api/v1/*/vaults", Method: "GET"},
		"lowercase method":  {Subject: "reader", Domain: "*", Path: "/api/v1/tenants", Method: "get"},
	}
	for name, policy := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{policy}); err == nil {
				t.Fatal("unsafe policy unexpectedly accepted")
			}
		})
	}
	if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{Subject: "reader", Domain: "*", Path: "/api/v1/tenants", Method: "GET"}}); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
	if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{Subject: "admin", Domain: "*", Path: "/internal/v1/authorization", Method: "PUT"}}); err != nil {
		t.Fatalf("internal administration policy rejected: %v", err)
	}
}

func TestStaticAuthorizerHonorsContextCancellation(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := authorizer.Authorize(ctx, "principal", "*", "/api/v1", "GET"); err == nil {
		t.Fatal("canceled authorization unexpectedly succeeded")
	}
}

func TestStaticAuthorizerReloadsAtomically(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1", Method: "GET",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorizer.Reload(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1/tenants", Method: "GET",
	}}); err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.Authorize(t.Context(), "reader", "*", "/api/v1", "GET")
	if err != nil || allowed {
		t.Fatalf("old policy remained active: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(t.Context(), "reader", "*", "/api/v1/tenants", "GET")
	if err != nil || !allowed {
		t.Fatalf("reloaded policy denied: allowed=%t err=%v", allowed, err)
	}
	if err := authorizer.Reload(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1/unsafe?query", Method: "GET",
	}}); err == nil {
		t.Fatal("invalid reload unexpectedly succeeded")
	}
	allowed, err = authorizer.Authorize(t.Context(), "reader", "*", "/api/v1/tenants", "GET")
	if err != nil || !allowed {
		t.Fatalf("failed reload discarded previous policy: allowed=%t err=%v", allowed, err)
	}
}
