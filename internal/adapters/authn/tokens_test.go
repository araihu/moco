package authn_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/araihu/moco/internal/adapters/authn"
)

func TestTokenAuthenticatorResolvesMultiplePrincipals(t *testing.T) {
	t.Parallel()
	first := "first-bearer-token-with-at-least-32-bytes"
	second := "second-bearer-token-with-at-least-32-bytes"
	authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{
		{PrincipalID: "operator-a", TokenSHA256: digest(first)},
		{PrincipalID: "controller-b", TokenSHA256: digest(second)},
	})
	if err != nil {
		t.Fatalf("construct authenticator: %v", err)
	}
	if principal, ok := authenticator.Authenticate(first); !ok || principal != "operator-a" {
		t.Fatalf("first credential resolved to %q, %t", principal, ok)
	}
	if principal, ok := authenticator.Authenticate(second); !ok || principal != "controller-b" {
		t.Fatalf("second credential resolved to %q, %t", principal, ok)
	}
	if principal, ok := authenticator.Authenticate("unknown-bearer-token"); ok || principal != "" {
		t.Fatalf("unknown credential resolved to %q, %t", principal, ok)
	}
}

func TestTokenAuthenticatorRejectsAmbiguousConfiguration(t *testing.T) {
	t.Parallel()
	digestValue := digest("token-with-at-least-32-bytes-for-tests")
	tests := map[string][]authn.Credential{
		"missing": nil,
		"duplicate principal": {
			{PrincipalID: "same", TokenSHA256: digestValue},
			{PrincipalID: "same", TokenSHA256: digest("different-token-with-at-least-32-bytes")},
		},
		"duplicate token": {
			{PrincipalID: "first", TokenSHA256: digestValue},
			{PrincipalID: "second", TokenSHA256: digestValue},
		},
		"uppercase digest":  {{PrincipalID: "first", TokenSHA256: strings.ToUpper(digestValue)}},
		"malformed digest":  {{PrincipalID: "first", TokenSHA256: "not-a-digest"}},
		"invalid principal": {{PrincipalID: "", TokenSHA256: digestValue}},
	}
	for name, values := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := authn.NewTokenAuthenticator(values); err == nil {
				t.Fatal("ambiguous or malformed configuration unexpectedly accepted")
			}
		})
	}
}

func TestTokenAuthenticatorReloadsAtomically(t *testing.T) {
	t.Parallel()
	first := "first-bearer-token-with-at-least-32-bytes"
	second := "second-bearer-token-with-at-least-32-bytes"
	authenticator, err := authn.NewTokenAuthenticator([]authn.Credential{{
		PrincipalID: "first", TokenSHA256: digest(first),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := authenticator.Reload([]authn.Credential{{PrincipalID: "second", TokenSHA256: digest(second)}}); err != nil {
		t.Fatal(err)
	}
	if principal, ok := authenticator.Authenticate(first); ok || principal != "" {
		t.Fatalf("old credential remained active as %q, %t", principal, ok)
	}
	if principal, ok := authenticator.Authenticate(second); !ok || principal != "second" {
		t.Fatalf("reloaded credential resolved to %q, %t", principal, ok)
	}
	if err := authenticator.Reload([]authn.Credential{{PrincipalID: "bad", TokenSHA256: "invalid"}}); err == nil {
		t.Fatal("invalid reload unexpectedly succeeded")
	}
	if principal, ok := authenticator.Authenticate(second); !ok || principal != "second" {
		t.Fatalf("failed reload discarded previous keyring: %q, %t", principal, ok)
	}
}

func digest(token string) string {
	value := sha256.Sum256([]byte(token))
	return hex.EncodeToString(value[:])
}
