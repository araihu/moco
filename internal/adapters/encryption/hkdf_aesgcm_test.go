package encryption_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/encryption"
)

func TestHKDFAESGCMEnvelopeRoundTripAndScopeBinding(t *testing.T) {
	t.Parallel()
	rootKey := bytes.Repeat([]byte{0x21}, 32)
	envelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v1", RootKey: rootKey,
	})
	if err != nil {
		t.Fatalf("create envelope: %v", err)
	}
	key, err := envelope.NewVaultKey("tenant-a", "vault-a", time.Now())
	if err != nil {
		t.Fatalf("create vault key: %v", err)
	}
	plaintext := []byte("correct horse battery staple")
	value, err := envelope.EncryptSecret(
		key, "tenant-a", "vault-a", "prod/db/password",
		"sha256:digest", "text/plain", plaintext,
	)
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	secondValue, err := envelope.EncryptSecret(
		key, "tenant-a", "vault-a", "prod/db/password",
		"sha256:digest", "text/plain", plaintext,
	)
	if err != nil {
		t.Fatalf("encrypt secret again: %v", err)
	}
	if len(key.Salt) != 32 || len(value.Salt) != 32 || bytes.Equal(value.Salt, secondValue.Salt) {
		t.Fatalf("expected independent random 256-bit salts, got %x and %x", value.Salt, secondValue.Salt)
	}
	decrypted, err := envelope.DecryptSecret(
		key, "tenant-a", "vault-a", "prod/db/password",
		"sha256:digest", "text/plain", value,
	)
	if err != nil {
		t.Fatalf("decrypt secret: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("round trip changed plaintext: %q", decrypted)
	}

	tamperedSalt := value
	tamperedSalt.Salt = append([]byte(nil), value.Salt...)
	tamperedSalt.Salt[0] ^= 0xff
	if _, err := envelope.DecryptSecret(
		key, "tenant-a", "vault-a", "prod/db/password",
		"sha256:digest", "text/plain", tamperedSalt,
	); err == nil {
		t.Fatal("tampered KDF salt unexpectedly authenticated")
	}
	tamperedCiphertext := value
	tamperedCiphertext.Ciphertext = append([]byte(nil), value.Ciphertext...)
	tamperedCiphertext.Ciphertext[0] ^= 0xff
	if _, err := envelope.DecryptSecret(
		key, "tenant-a", "vault-a", "prod/db/password",
		"sha256:digest", "text/plain", tamperedCiphertext,
	); err == nil {
		t.Fatal("tampered ciphertext unexpectedly authenticated")
	}

	tests := map[string]struct {
		tenantID    string
		vaultID     string
		path        string
		digest      string
		contentType string
	}{
		"tenant":      {"tenant-b", "vault-a", "prod/db/password", "sha256:digest", "text/plain"},
		"vault":       {"tenant-a", "vault-b", "prod/db/password", "sha256:digest", "text/plain"},
		"path":        {"tenant-a", "vault-a", "prod/db/other", "sha256:digest", "text/plain"},
		"digest":      {"tenant-a", "vault-a", "prod/db/password", "sha256:changed", "text/plain"},
		"contentType": {"tenant-a", "vault-a", "prod/db/password", "sha256:digest", "application/json"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := envelope.DecryptSecret(
				key, test.tenantID, test.vaultID, test.path, test.digest, test.contentType, value,
			)
			if err == nil {
				t.Fatal("metadata substitution unexpectedly authenticated")
			}
		})
	}
}

func TestHKDFAESGCMEnvelopeRejectsUnavailableOrWrongRootKey(t *testing.T) {
	t.Parallel()
	first, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v1", RootKey: bytes.Repeat([]byte{0x11}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := first.NewVaultKey("tenant", "vault", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := first.EncryptSecret(key, "tenant", "vault", "path", "digest", "text/plain", []byte("value"))
	if err != nil {
		t.Fatal(err)
	}

	wrongKey, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v1", RootKey: bytes.Repeat([]byte{0x22}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongKey.DecryptSecret(key, "tenant", "vault", "path", "digest", "text/plain", value); err == nil {
		t.Fatal("wrong root key unexpectedly decrypted the vault key")
	}

	wrongID, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v2", RootKey: bytes.Repeat([]byte{0x11}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrongID.DecryptSecret(key, "tenant", "vault", "path", "digest", "text/plain", value); err == nil {
		t.Fatal("unavailable root key ID unexpectedly decrypted the vault key")
	}
}
