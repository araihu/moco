package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/core/ports"
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

func TestBuildSecurityRuntimeBootstrapsAndUsesPersistedSnapshot(t *testing.T) {
	t.Parallel()
	token := "controller-token-with-at-least-32-bytes"
	digest := sha256.Sum256([]byte(token))
	access := authorizationConfiguration{
		Principals:   []authn.Credential{{PrincipalID: "controller", TokenSHA256: hex.EncodeToString(digest[:])}},
		RoleBindings: []authz.RoleBinding{{Principal: "controller", Role: "secret-reader", Domain: "tenant-a"}},
		Policies:     []authz.Policy{{Subject: "secret-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "GET"}},
	}
	path := writeAuthorizationConfig(t, access)
	repository := &runtimeAuthorizationRepository{}
	runtime, err := buildSecurityRuntime(context.Background(), configuration{authConfigPath: path}, repository)
	if err != nil {
		t.Fatalf("bootstrap configured security: %v", err)
	}
	if runtime.reloader == nil || runtime.bus == nil {
		t.Fatal("persisted security runtime did not initialize reload lifecycle")
	}
	if repository.replaceCalls != 1 || !repository.state.Initialized {
		t.Fatalf("bootstrap state = %#v, replacements = %d", repository.state, repository.replaceCalls)
	}
	allowed, err := runtime.authorizer.Authorize(context.Background(), "controller", "tenant-a", "/api/v1/tenants/t/vaults/v/secret", "GET")
	if err != nil || !allowed {
		t.Fatalf("bootstrapped policy denied: allowed=%t, err=%v", allowed, err)
	}
	runtime.close()

	// A restart must use the initialized snapshot, not silently replace it
	// with a changed deployment file.
	access.Policies = []authz.Policy{{Subject: "secret-reader", Domain: "tenant-a", Path: "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret", Method: "PUT"}}
	if err := os.WriteFile(path, mustMarshalJSON(t, access), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := buildSecurityRuntime(context.Background(), configuration{authConfigPath: path}, repository)
	if err != nil {
		t.Fatalf("restart configured security: %v", err)
	}
	t.Cleanup(restarted.close)
	if repository.replaceCalls != 1 {
		t.Fatalf("initialized snapshot was unexpectedly reseeded: %d replacements", repository.replaceCalls)
	}
	allowed, err = restarted.authorizer.Authorize(context.Background(), "controller", "tenant-a", "/api/v1/tenants/t/vaults/v/secret", "GET")
	if err != nil || !allowed {
		t.Fatalf("persisted policy was not retained: allowed=%t, err=%v", allowed, err)
	}
	allowed, err = restarted.authorizer.Authorize(context.Background(), "controller", "tenant-a", "/api/v1/tenants/t/vaults/v/secret", "PUT")
	if err != nil || allowed {
		t.Fatalf("changed file policy unexpectedly replaced snapshot: allowed=%t, err=%v", allowed, err)
	}
}

func TestBuildSecurityRuntimeRejectsInvalidBootstrapWithoutPersisting(t *testing.T) {
	t.Parallel()
	token := "controller-token-with-at-least-32-bytes"
	digest := sha256.Sum256([]byte(token))
	access := authorizationConfiguration{
		Principals: []authn.Credential{{PrincipalID: "controller", TokenSHA256: hex.EncodeToString(digest[:])}},
		Policies:   []authz.Policy{{Subject: "controller", Domain: "tenant-a", Path: "/api/v1/unsafe?query", Method: "GET"}},
	}
	repository := &runtimeAuthorizationRepository{}
	path := writeAuthorizationConfig(t, access)
	if _, err := buildSecurityRuntime(context.Background(), configuration{authConfigPath: path}, repository); err == nil {
		t.Fatal("invalid bootstrap policy unexpectedly accepted")
	}
	if repository.replaceCalls != 0 || repository.state.Initialized {
		t.Fatalf("invalid bootstrap changed repository: %#v, replacements=%d", repository.state, repository.replaceCalls)
	}
}

func writeAuthorizationConfig(t *testing.T, access authorizationConfiguration) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, mustMarshalJSON(t, access), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustMarshalJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

type runtimeAuthorizationRepository struct {
	state        ports.AuthorizationState
	replaceCalls int
}

func (repository *runtimeAuthorizationRepository) LoadAuthorization(context.Context) (ports.AuthorizationState, error) {
	return repository.state, nil
}

func (repository *runtimeAuthorizationRepository) ReplaceAuthorization(_ context.Context, state ports.AuthorizationState) error {
	if repository.state.Initialized {
		return errors.New("initialized snapshot unexpectedly replaced")
	}
	repository.state = ports.AuthorizationState{
		Initialized:  true,
		RoleBindings: append([]ports.AuthorizationRoleBinding(nil), state.RoleBindings...),
		Policies:     append([]ports.AuthorizationPolicy(nil), state.Policies...),
	}
	repository.replaceCalls++
	return nil
}
