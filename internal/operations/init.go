package operations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/araihu/moco/internal/adapters/authn"
	"github.com/araihu/moco/internal/core/ports"
)

// Initialize generates independent credentials in a new private directory. TLS
// issuance and independently protected recovery custody remain operator tasks.
func Initialize(directory string) (result error) {
	if err := os.Mkdir(directory, 0o700); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			_ = os.RemoveAll(directory)
		}
	}()
	values := make(map[string][]byte)
	for _, name := range []string{"admin-token", "cursor-key", "metrics-token"} {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		values[name] = []byte(hex.EncodeToString(key))
		clear(key)
	}
	defer func() {
		for _, value := range values {
			clear(value)
		}
	}()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	keyring, err := json.Marshal(map[string]any{"activeKeyId": "root-v1", "keys": map[string]string{"root-v1": base64.StdEncoding.EncodeToString(key)}})
	clear(key)
	if err != nil {
		return err
	}
	values["keyring.json"] = keyring
	digest := sha256.Sum256(values["admin-token"])
	policies := []ports.AuthorizationPolicy{{Subject: "bootstrap-admin", Domain: "*", Path: "/api/v1", Method: "GET"}, {Subject: "bootstrap-admin", Domain: "*", Path: "/api/v1/*", Method: "*"}}
	for route, methods := range map[string][]string{
		"/internal/v1/authorization": {"GET", "PUT"}, "/internal/v1/audit": {"GET"},
		"/internal/v1/audit/retention": {"POST"}, "/internal/v1/encryption/rotation": {"POST"}, "/internal/v1/encryption/status": {"GET"},
	} {
		for _, method := range methods {
			policies = append(policies, ports.AuthorizationPolicy{Subject: "bootstrap-admin", Domain: "*", Path: route, Method: method})
		}
	}
	access, err := json.Marshal(struct {
		Principals []authn.Credential          `json:"principals"`
		Policies   []ports.AuthorizationPolicy `json:"policies"`
	}{[]authn.Credential{{PrincipalID: "bootstrap-admin", TokenSHA256: hex.EncodeToString(digest[:])}}, policies})
	if err != nil {
		return err
	}
	values["access.json"] = access
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(directory, name), value, 0o600); err != nil {
			return err
		}
	}
	return syncDirectory(directory)
}
