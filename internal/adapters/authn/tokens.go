// Package authn implements bearer-token authentication adapters.
package authn

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/araihu/moco/internal/core/ports"
)

// Credential identifies a principal by the SHA-256 digest of its bearer token.
// The clear token is intentionally not part of the configuration shape.
type Credential = ports.AuthorizationPrincipal

type credential struct {
	principalID string
	digest      [sha256.Size]byte
}

// TokenAuthenticator verifies bearer tokens without retaining clear tokens.
type TokenAuthenticator struct {
	mu          sync.RWMutex
	credentials []credential
}

// NewTokenAuthenticator validates and constructs a token keyring.
func NewTokenAuthenticator(values []Credential) (*TokenAuthenticator, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one bearer credential is required")
	}
	credentials := make([]credential, 0, len(values))
	seenIDs := make(map[string]struct{}, len(values))
	seenDigests := make(map[[sha256.Size]byte]struct{}, len(values))
	for index, value := range values {
		if !validIdentifier(value.PrincipalID) {
			return nil, fmt.Errorf("credential %d principal ID must contain 1 to 128 visible ASCII characters", index)
		}
		if _, exists := seenIDs[value.PrincipalID]; exists {
			return nil, fmt.Errorf("credential %d duplicates principal ID %q", index, value.PrincipalID)
		}
		if len(value.TokenSHA256) != sha256.Size*2 || value.TokenSHA256 != strings.ToLower(value.TokenSHA256) {
			return nil, fmt.Errorf("credential %d tokenSha256 must be 64 lowercase hexadecimal characters", index)
		}
		decoded, err := hex.DecodeString(value.TokenSHA256)
		if err != nil {
			return nil, fmt.Errorf("credential %d tokenSha256 is invalid: %w", index, err)
		}
		var digest [sha256.Size]byte
		copy(digest[:], decoded)
		if _, exists := seenDigests[digest]; exists {
			return nil, fmt.Errorf("credential %d duplicates a token digest", index)
		}
		seenIDs[value.PrincipalID] = struct{}{}
		seenDigests[digest] = struct{}{}
		credentials = append(credentials, credential{principalID: value.PrincipalID, digest: digest})
	}
	return &TokenAuthenticator{credentials: credentials}, nil
}

// Reload replaces the keyring only after the complete candidate is valid.
func (a *TokenAuthenticator) Reload(values []Credential) error {
	candidate, err := NewTokenAuthenticator(values)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.credentials = candidate.credentials
	a.mu.Unlock()
	return nil
}

// Authenticate returns the configured principal for one bearer token.
func (a *TokenAuthenticator) Authenticate(token string) (string, bool) {
	digest := sha256.Sum256([]byte(token))
	principal := ""
	found := 0
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, credential := range a.credentials {
		matched := subtle.ConstantTimeCompare(digest[:], credential.digest[:])
		if matched == 1 {
			principal = credential.principalID
		}
		found |= matched
	}
	return principal, found == 1
}

func validIdentifier(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}
