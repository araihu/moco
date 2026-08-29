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

func TestHKDFAESGCMKeyringReadsPreviousEraAndRewraps(t *testing.T) {
	t.Parallel()
	oldRoot := bytes.Repeat([]byte{0x31}, 32)
	newRoot := bytes.Repeat([]byte{0x42}, 32)
	oldEnvelope, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v1", RootKey: oldRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := oldEnvelope.NewVaultKey("tenant-a", "vault-a", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := oldEnvelope.EncryptSecret(key, "tenant-a", "vault-a", "prod/password", "sha256:value", "text/plain", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	keyring, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{
		RootKeyID: "root-v2",
		RootKeys:  map[string][]byte{"root-v1": oldRoot, "root-v2": newRoot},
	})
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := keyring.DecryptSecret(key, "tenant-a", "vault-a", "prod/password", "sha256:value", "text/plain", value)
	if err != nil || !bytes.Equal(decrypted, []byte("secret")) {
		t.Fatalf("previous key era did not decrypt: value=%q err=%v", decrypted, err)
	}
	rotated, err := keyring.RewrapVaultKey("tenant-a", "vault-a", key)
	if err != nil {
		t.Fatalf("rewrap key: %v", err)
	}
	if rotated.RootKeyID != "root-v2" || bytes.Equal(rotated.Salt, key.Salt) || bytes.Equal(rotated.Ciphertext, key.Ciphertext) {
		t.Fatalf("rewrap did not produce fresh active-era material: old=%#v new=%#v", key, rotated)
	}
	decrypted, err = keyring.DecryptSecret(rotated, "tenant-a", "vault-a", "prod/password", "sha256:value", "text/plain", value)
	if err != nil || !bytes.Equal(decrypted, []byte("secret")) {
		t.Fatalf("rotated key did not preserve secret: value=%q err=%v", decrypted, err)
	}

	activeOnly, err := encryption.NewHKDFAESGCMEnvelope(encryption.HKDFAESGCMOptions{RootKeyID: "root-v2", RootKey: newRoot})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activeOnly.DecryptSecret(key, "tenant-a", "vault-a", "prod/password", "sha256:value", "text/plain", value); err == nil {
		t.Fatal("active-only key unexpectedly decrypted an old-era vault key")
	}
}

func TestHKDFAESGCMKeyringRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	valid := bytes.Repeat([]byte{0x01}, 32)
	tests := []encryption.HKDFAESGCMOptions{
		{RootKeyID: "root-v2", RootKey: valid, RootKeys: map[string][]byte{"root-v2": valid}},
		{RootKeyID: "root-v2", RootKeys: map[string][]byte{"root-v1": valid}},
		{RootKeyID: "root-v2", RootKeys: map[string][]byte{"root-v2": valid[:31]}},
		{RootKeyID: "root-v2", RootKeys: map[string][]byte{}},
	}
	for index, options := range tests {
		if _, err := encryption.NewHKDFAESGCMEnvelope(options); err == nil {
			t.Fatalf("invalid keyring case %d unexpectedly accepted", index)
		}
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
