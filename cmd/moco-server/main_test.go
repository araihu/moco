package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/authz"
)

func TestLoadConfigurationDecodesEncryptionKey(t *testing.T) {
	rootKey := bytes.Repeat([]byte{0x5a}, 32)
	t.Setenv("MOCO_BEARER_TOKEN", "test-bearer-token-with-at-least-32-bytes")
	t.Setenv("MOCO_AUTH_CONFIG", "")
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")
	t.Setenv("MOCO_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(rootKey))
	t.Setenv("MOCO_ENCRYPTION_KEY_ID", "")

	configuration, err := loadConfiguration()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	if configuration.encryptionKeyID != "local-v1" || !bytes.Equal(configuration.encryptionKey, rootKey) {
		t.Fatalf("unexpected encryption configuration: ID %q, %d key bytes", configuration.encryptionKeyID, len(configuration.encryptionKey))
	}
}

func TestLoadConfigurationRejectsMalformedEncryptionKey(t *testing.T) {
	t.Setenv("MOCO_BEARER_TOKEN", "test-bearer-token-with-at-least-32-bytes")
	t.Setenv("MOCO_AUTH_CONFIG", "")
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")

	for name, value := range map[string]string{
		"missing":    "",
		"not-base64": "not base64",
		"short":      base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 31)),
		"raw":        base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32)),
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MOCO_ENCRYPTION_KEY", value)
			if _, err := loadConfiguration(); err == nil {
				t.Fatal("malformed encryption key unexpectedly accepted")
			}
		})
	}
}

func TestLoadConfigurationAcceptsAuthConfigWithoutLegacyToken(t *testing.T) {
	t.Setenv("MOCO_BEARER_TOKEN", "")
	t.Setenv("MOCO_AUTH_CONFIG", filepath.Join(t.TempDir(), "access.json"))
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")
	t.Setenv("MOCO_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 32)))
	if _, err := loadConfiguration(); err != nil {
		t.Fatalf("auth config should replace legacy token: %v", err)
	}
}

func TestLoadConfigurationRejectsAmbiguousAuthenticationSettings(t *testing.T) {
	t.Setenv("MOCO_BEARER_TOKEN", "test-bearer-token-with-at-least-32-bytes")
	t.Setenv("MOCO_AUTH_CONFIG", filepath.Join(t.TempDir(), "access.json"))
	t.Setenv("MOCO_CURSOR_HMAC_KEY", "test-cursor-key-with-at-least-32-bytes")
	t.Setenv("MOCO_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 32)))
	if _, err := loadConfiguration(); err == nil {
		t.Fatal("legacy and configured authentication unexpectedly accepted together")
	}
}

func TestConfiguredSecurityBuildsStablePrincipalsAndPolicies(t *testing.T) {
	t.Parallel()
	token := "controller-token-with-at-least-32-bytes"
	digest := sha256.Sum256([]byte(token))
	access := authorizationConfiguration{
		Principals:   []authn.Credential{{PrincipalID: "controller", TokenSHA256: hex.EncodeToString(digest[:])}},
		RoleBindings: []authz.RoleBinding{{Principal: "controller", Role: "secret-reader", Domain: "tenant-a"}},
		Policies:     []authz.Policy{{Subject: "secret-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET"}},
	}
	// JSON round-tripping keeps this test coupled to the deployment-facing file shape.
	payload, err := json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	authenticator, authorizer, err := buildSecurity(configuration{authConfigPath: path})
	if err != nil {
		t.Fatalf("build configured security: %v", err)
	}
	if principal, ok := authenticator.Authenticate(token); !ok || principal != "controller" {
		t.Fatalf("configured token resolved to %q, %t", principal, ok)
	}
	allowed, err := authorizer.Authorize(t.Context(), "controller", "tenant-a", "/api/v1/tenants/t/vaults/v/secret", "GET")
	if err != nil || !allowed {
		t.Fatalf("configured policy denied: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(t.Context(), "controller", "tenant-a", "/api/v1/tenants/t/vaults/v/secret", "PUT")
	if err != nil || allowed {
		t.Fatalf("configured default-deny failed: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = authorizer.Authorize(t.Context(), "controller", "tenant-b", "/api/v1/tenants/t/vaults/v/secret", "GET")
	if err != nil || allowed {
		t.Fatalf("configured tenant domain isolation failed: allowed=%t, err=%v", allowed, err)
	}
}

func TestLoadAuthorizationConfigurationRejectsMalformedDocuments(t *testing.T) {
	t.Parallel()
	valid := `{"principals":[{"id":"p","tokenSha256":"0000000000000000000000000000000000000000000000000000000000000000"}]}`
	tests := map[string]string{
		"unknown field":    valid[:len(valid)-1] + `,"unknown":true}`,
		"trailing value":   valid + ` {}`,
		"empty principals": `{"principals":[]}`,
	}
	for name, payload := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "access.json")
			if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadAuthorizationConfiguration(path); err == nil {
				t.Fatal("malformed authorization configuration unexpectedly accepted")
			}
		})
	}
}
