// Package encryption implements authenticated envelope encryption adapters.
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

const (
	encryptionKeyBytes = 32
	kdfSaltBytes       = sha256.Size
)

var singleUseNonce [12]byte

// HKDFAESGCMOptions configures one deployment root key and deterministic test seam.
type HKDFAESGCMOptions struct {
	RootKeyID string
	RootKey   []byte
	Random    io.Reader
}

// HKDFAESGCMEnvelope derives an independent AES-256-GCM key for every wrapped
// vault key and secret ciphertext. Each derived key encrypts exactly one message.
type HKDFAESGCMEnvelope struct {
	rootKeyID string
	rootKey   []byte
	random    io.Reader
}

// NewHKDFAESGCMEnvelope constructs an envelope cipher. The caller retains
// ownership of options.RootKey and may clear it after this function returns.
func NewHKDFAESGCMEnvelope(options HKDFAESGCMOptions) (*HKDFAESGCMEnvelope, error) {
	if !validKeyID(options.RootKeyID) {
		return nil, errors.New("root key ID must contain 1 to 128 visible ASCII characters")
	}
	if len(options.RootKey) != encryptionKeyBytes {
		return nil, errors.New("root encryption key must contain exactly 32 bytes")
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &HKDFAESGCMEnvelope{
		rootKeyID: options.RootKeyID,
		rootKey:   append([]byte(nil), options.RootKey...),
		random:    options.Random,
	}, nil
}

// Destroy clears the in-process root key after all encryption operations stop.
func (e *HKDFAESGCMEnvelope) Destroy() {
	wipe(e.rootKey)
	e.rootKey = nil
}

// NewVaultKey generates and wraps one independent vault data key.
func (e *HKDFAESGCMEnvelope) NewVaultKey(tenantID, vaultID string, createdAt time.Time) (ports.WrappedVaultKey, error) {
	dataKey := make([]byte, encryptionKeyBytes)
	if _, err := io.ReadFull(e.random, dataKey); err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("generate vault data key: %w", err)
	}
	defer wipe(dataKey)
	salt, err := e.randomSalt()
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("generate vault key salt: %w", err)
	}
	aad := associatedData("vault-key", e.rootKeyID, tenantID, vaultID)
	defer wipe(aad)
	wrappingCipher, err := deriveAESGCM(e.rootKey, salt, aad)
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("derive vault wrapping key: %w", err)
	}
	wrapped := wrappingCipher.Seal(nil, singleUseNonce[:], dataKey, aad)
	return ports.WrappedVaultKey{
		RootKeyID:  e.rootKeyID,
		Salt:       salt,
		Ciphertext: wrapped,
		CreatedAt:  createdAt.UTC(),
	}, nil
}

// EncryptSecret encrypts a value under a one-time key derived from its vault
// data key. Metadata supplied to this method is authenticated.
func (e *HKDFAESGCMEnvelope) EncryptSecret(
	key ports.WrappedVaultKey,
	tenantID, vaultID, path, digest, contentType string,
	plaintext []byte,
) (ports.EncryptedSecretValue, error) {
	dataKey, err := e.unwrapVaultKey(key, tenantID, vaultID)
	if err != nil {
		return ports.EncryptedSecretValue{}, err
	}
	defer wipe(dataKey)
	salt, err := e.randomSalt()
	if err != nil {
		return ports.EncryptedSecretValue{}, fmt.Errorf("generate secret key salt: %w", err)
	}
	aad := associatedData("secret", tenantID, vaultID, path, digest, contentType)
	defer wipe(aad)
	valueCipher, err := deriveAESGCM(dataKey, salt, aad)
	if err != nil {
		return ports.EncryptedSecretValue{}, fmt.Errorf("derive secret encryption key: %w", err)
	}
	ciphertext := valueCipher.Seal(nil, singleUseNonce[:], plaintext, aad)
	return ports.EncryptedSecretValue{Salt: salt, Ciphertext: ciphertext}, nil
}

// DecryptSecret authenticates metadata and decrypts one secret value.
func (e *HKDFAESGCMEnvelope) DecryptSecret(
	key ports.WrappedVaultKey,
	tenantID, vaultID, path, digest, contentType string,
	value ports.EncryptedSecretValue,
) ([]byte, error) {
	if len(value.Salt) != kdfSaltBytes || len(value.Ciphertext) < aes.BlockSize {
		return nil, errors.New("stored secret ciphertext has an invalid shape")
	}
	dataKey, err := e.unwrapVaultKey(key, tenantID, vaultID)
	if err != nil {
		return nil, err
	}
	defer wipe(dataKey)
	aad := associatedData("secret", tenantID, vaultID, path, digest, contentType)
	defer wipe(aad)
	valueCipher, err := deriveAESGCM(dataKey, value.Salt, aad)
	if err != nil {
		return nil, fmt.Errorf("derive secret decryption key: %w", err)
	}
	plaintext, err := valueCipher.Open(nil, singleUseNonce[:], value.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("authenticate secret ciphertext: %w", err)
	}
	return plaintext, nil
}

func (e *HKDFAESGCMEnvelope) unwrapVaultKey(key ports.WrappedVaultKey, tenantID, vaultID string) ([]byte, error) {
	if key.RootKeyID != e.rootKeyID {
		return nil, errors.New("stored vault key uses an unavailable root key ID")
	}
	if len(key.Salt) != kdfSaltBytes || len(key.Ciphertext) != encryptionKeyBytes+aes.BlockSize {
		return nil, errors.New("stored wrapped vault key has an invalid shape")
	}
	aad := associatedData("vault-key", key.RootKeyID, tenantID, vaultID)
	defer wipe(aad)
	wrappingCipher, err := deriveAESGCM(e.rootKey, key.Salt, aad)
	if err != nil {
		return nil, fmt.Errorf("derive vault unwrapping key: %w", err)
	}
	dataKey, err := wrappingCipher.Open(nil, singleUseNonce[:], key.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("authenticate wrapped vault key: %w", err)
	}
	if len(dataKey) != encryptionKeyBytes {
		wipe(dataKey)
		return nil, errors.New("unwrapped vault key has an invalid length")
	}
	return dataKey, nil
}

func (e *HKDFAESGCMEnvelope) randomSalt() ([]byte, error) {
	salt := make([]byte, kdfSaltBytes)
	if _, err := io.ReadFull(e.random, salt); err != nil {
		return nil, err
	}
	return salt, nil
}

func deriveAESGCM(secret, salt, aad []byte) (cipher.AEAD, error) {
	derivedKey, err := hkdf.Key(sha256.New, secret, salt, string(aad), encryptionKeyBytes)
	if err != nil {
		return nil, err
	}
	defer wipe(derivedKey)
	block, err := aes.NewCipher(derivedKey)
	runtime.KeepAlive(derivedKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func associatedData(kind string, fields ...string) []byte {
	const format = "moco-envelope-hkdf-sha256-aes256gcm-v1"
	size := 4 + len(format) + 4 + len(kind)
	for _, field := range fields {
		size += 4 + len(field)
	}
	result := make([]byte, 0, size)
	result = appendField(result, format)
	result = appendField(result, kind)
	for _, field := range fields {
		result = appendField(result, field)
	}
	return result
}

func appendField(target []byte, value string) []byte {
	var length [4]byte
	// #nosec G115 -- all AAD fields are contract-bounded far below uint32.
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	target = append(target, length[:]...)
	return append(target, value...)
}

func validKeyID(value string) bool {
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

func wipe(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}
