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

func TestStaticAuthorizerNarrowSecretPoliciesByLogicalPath(t *testing.T) {
	t.Parallel()
	prefix := "prod/database/"
	authorizer, err := authz.NewStaticAuthorizer(
		[]authz.RoleBinding{{Principal: "operator", Role: "secret-reader", Domain: "tenant-a"}},
		[]authz.Policy{{
			Subject: "secret-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET",
			SecretPathPrefix: &prefix,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	resource := "/api/v1/tenants/tenant-a/vaults/vault-a/secret"
	allowed, err := authorizer.AuthorizeSecretPath(t.Context(), "operator", "tenant-a", resource, "GET", "prod/database/password")
	if err != nil || !allowed {
		t.Fatalf("prefixed secret was denied: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeSecretPath(t.Context(), "operator", "tenant-a", resource, "GET", "dev/database/password")
	if err != nil || allowed {
		t.Fatalf("out-of-prefix secret was allowed: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(t.Context(), "operator", "tenant-a", resource, "GET")
	if err != nil || allowed {
		t.Fatalf("path-scoped policy bypassed by route-only authorization: allowed=%t err=%v", allowed, err)
	}
}

func TestStaticAuthorizerPreservesLegacyWildcardSecretPrefix(t *testing.T) {
	t.Parallel()
	prefix := "*"
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET", SecretPathPrefix: &prefix,
	}})
	if err != nil {
		t.Fatal(err)
	}
	resource := "/api/v1/tenants/tenant-a/vaults/vault-a/secret"
	allowed, err := authorizer.AuthorizeSecretPath(t.Context(), "reader", "tenant-a", resource, "GET", "prod/literal")
	if err != nil || !allowed {
		t.Fatalf("legacy wildcard prefix was denied: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeSecretPath(t.Context(), "reader", "tenant-a", resource, "GET", "dev/literal")
	if err != nil || !allowed {
		t.Fatalf("legacy wildcard prefix did not preserve broad access: allowed=%t err=%v", allowed, err)
	}
}

func TestStaticAuthorizerNarrowSecretMetadataAndCollections(t *testing.T) {
	t.Parallel()
	prefix := "prod/database/"
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{
		{Subject: "reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret/metadata", Method: "GET", SecretPathPrefix: &prefix},
		{Subject: "reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets", Method: "GET", SecretPathPrefix: &prefix},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadataResource := "/api/v1/tenants/tenant-a/vaults/vault-a/secret/metadata"
	allowed, err := authorizer.AuthorizeSecretPath(t.Context(), "reader", "tenant-a", metadataResource, "GET", "prod/database/password")
	if err != nil || !allowed {
		t.Fatalf("prefixed metadata was denied: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeSecretPath(t.Context(), "reader", "tenant-a", metadataResource, "GET", "prod/other/password")
	if err != nil || allowed {
		t.Fatalf("out-of-prefix metadata was allowed: allowed=%t err=%v", allowed, err)
	}
	collectionResource := "/api/v1/tenants/tenant-a/vaults/vault-a/secrets"
	allowed, err = authorizer.AuthorizeSecretPrefix(t.Context(), "reader", "tenant-a", collectionResource, "GET", "prod/database/passwords/")
	if err != nil || !allowed {
		t.Fatalf("nested collection prefix was denied: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeSecretPrefix(t.Context(), "reader", "tenant-a", collectionResource, "GET", "")
	if err != nil || allowed {
		t.Fatalf("empty collection prefix was allowed: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeSecretPrefix(t.Context(), "reader", "tenant-a", collectionResource, "GET", "dev/")
	if err != nil || allowed {
		t.Fatalf("out-of-prefix collection was allowed: allowed=%t err=%v", allowed, err)
	}
}

func TestStaticAuthorizerFindsScopedCollectionAccessAndTenantVisibility(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(
		[]authz.RoleBinding{{Principal: "operator", Role: "tenant-reader", Domain: "tenant-a"}},
		[]authz.Policy{{Subject: "tenant-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}", Method: "GET"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := authorizer.AuthorizeAnyDomain(t.Context(), "operator", "/api/v1/tenants/{tenantId}", "GET")
	if err != nil || !allowed {
		t.Fatalf("scoped collection access = %t, err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeTenant(t.Context(), "operator", "tenant-a")
	if err != nil || !allowed {
		t.Fatalf("visible tenant denied: allowed=%t err=%v", allowed, err)
	}
	allowed, err = authorizer.AuthorizeTenant(t.Context(), "operator", "tenant-b")
	if err != nil || allowed {
		t.Fatalf("foreign tenant unexpectedly visible: allowed=%t err=%v", allowed, err)
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
	if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{Subject: "auditor", Domain: "*", Path: "/internal/v1/audit", Method: "GET"}}); err != nil {
		t.Fatalf("internal audit policy rejected: %v", err)
	}
	prefix := "prod/"
	if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/vault", Method: "GET", SecretPathPrefix: &prefix,
	}}); err == nil {
		t.Fatal("secret path prefix on a non-secret route unexpectedly accepted")
	}
	emptyPrefix := ""
	if _, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{
		Subject: "reader", Domain: "*", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET", SecretPathPrefix: &emptyPrefix,
	}}); err == nil {
		t.Fatal("empty secret path prefix unexpectedly accepted")
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
