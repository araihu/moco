package ports

import (
	"context"
	"errors"
	"time"

	"github.com/araihu/moco/internal/core/domain"
)

var (
	// ErrSecretNotFound means the tenant/vault/path tuple is absent.
	ErrSecretNotFound = errors.New("secret not found")
	// ErrVaultKeyNotFound means an existing vault has no data key yet.
	ErrVaultKeyNotFound = errors.New("vault data key not found")
	// ErrSecretPrecondition means the persisted secret version changed.
	ErrSecretPrecondition = errors.New("secret version precondition failed")
	// ErrEncryptionKeyStateConflict means this process is using a stale or
	// mismatched configured root-key epoch and must not write encryption state.
	ErrEncryptionKeyStateConflict = errors.New("encryption key state conflict")
)

// EncryptionKeyState is the deployment-wide root-key era recorded in SQLite.
// Epochs fence stale server processes during a rolling key rotation.
type EncryptionKeyState struct {
	ActiveRootKeyID string
	Epoch           int64
}

// WrappedVaultKey is a random vault data key encrypted under a deployment root
// key. Key material is always wrapped before it reaches a repository.
type WrappedVaultKey struct {
	RootKeyID  string
	Salt       []byte
	Ciphertext []byte
	CreatedAt  time.Time
}

// VaultKeyRecord identifies one persisted vault data key without exposing
// plaintext or deployment root-key material.
type VaultKeyRecord struct {
	TenantID string
	VaultID  string
	Key      WrappedVaultKey
}

// ListVaultKeysQuery selects a keyset page ordered by tenant and vault ID.
// The pair is an exclusive checkpoint; both values must be supplied together
// when continuing a page sequence.
type ListVaultKeysQuery struct {
	AfterTenantID string
	AfterVaultID  string
	PageSize      int
}

// ReplaceVaultKeyCommand atomically replaces a wrapped key only if the
// persisted source material still matches Expected. This makes retries safe
// when another rotation worker wins the race first.
type ReplaceVaultKeyCommand struct {
	TenantID string
	VaultID  string
	Expected WrappedVaultKey
	Replace  WrappedVaultKey
	// ExpectedKeyState is checked in the same database mutation as the CAS.
	// A nil value keeps the repository useful for isolated, unfenced tests.
	ExpectedKeyState *EncryptionKeyState
}

// EncryptedSecretValue is one AEAD ciphertext and its non-secret KDF salt.
type EncryptedSecretValue struct {
	Salt       []byte
	Ciphertext []byte
}

// StoredSecret contains encrypted value material plus authenticated metadata.
type StoredSecret struct {
	Metadata domain.SecretMetadata
	Value    EncryptedSecretValue
	VaultKey WrappedVaultKey
}

// PutSecretCommand is the atomic persistence input for one encrypted upsert.
type PutSecretCommand struct {
	TenantID        string
	VaultID         string
	Path            string
	Digest          string
	ContentType     string
	Value           EncryptedSecretValue
	UpdatedAt       time.Time
	CreateOnly      bool
	ExpectedVersion *int64
	// ExpectedKeyState is checked in the same transaction as the secret write.
	ExpectedKeyState *EncryptionKeyState
}

// DeleteSecretCommand is the atomic persistence input for one secret delete.
type DeleteSecretCommand struct {
	TenantID        string
	VaultID         string
	Path            string
	ExpectedVersion *int64
	// ExpectedKeyState is checked in the same transaction as the delete.
	ExpectedKeyState *EncryptionKeyState
}

// PutSecretResult identifies creation versus replacement.
type PutSecretResult struct {
	Metadata domain.SecretMetadata
	Created  bool
}

// ListSecretsQuery selects metadata within one stable vault snapshot.
type ListSecretsQuery struct {
	TenantID         string
	VaultID          string
	Prefix           *string
	AfterSequence    int64
	SnapshotSequence int64
	PageSize         int
}

// SecretRepository persists only wrapped keys, ciphertext, and metadata.
type SecretRepository interface {
	GetVaultKey(context.Context, string, string) (WrappedVaultKey, error)
	CreateVaultKey(context.Context, string, string, WrappedVaultKey, *EncryptionKeyState) (WrappedVaultKey, error)
	PutSecret(context.Context, PutSecretCommand) (PutSecretResult, error)
	GetSecret(context.Context, string, string, string) (StoredSecret, error)
	GetSecretMetadata(context.Context, string, string, string) (domain.SecretMetadata, error)
	MaxSecretSequence(context.Context, string, string) (int64, error)
	ListSecretMetadata(context.Context, ListSecretsQuery) ([]domain.SecretMetadata, error)
	DeleteSecret(context.Context, DeleteSecretCommand) error
}

// VaultKeyRotationRepository exposes only the bounded persistence operations
// needed by online root-key rotation.
type VaultKeyRotationRepository interface {
	ListVaultKeys(context.Context, ListVaultKeysQuery) ([]VaultKeyRecord, error)
	ReplaceVaultKey(context.Context, ReplaceVaultKeyCommand) (bool, error)
	CountVaultKeysNotUsingRootKey(context.Context, string) (int64, error)
}

// EncryptionKeyStateRepository persists and reads the deployment-wide active
// root-key epoch used to fence stale writers.
type EncryptionKeyStateRepository interface {
	CurrentEncryptionKeyState(context.Context) (EncryptionKeyState, error)
	EnsureEncryptionKeyState(context.Context, string, int64) (EncryptionKeyState, error)
}

// EncryptionKeyStateReader is the read-only portion used by request services.
type EncryptionKeyStateReader interface {
	CurrentEncryptionKeyState(context.Context) (EncryptionKeyState, error)
}

// ActiveRootKeyIDProvider reports which key era this process uses for writes.
type ActiveRootKeyIDProvider interface {
	ActiveRootKeyID() string
}

// SecretCipher performs the envelope-encryption operations used by the core
// service without exposing deployment key material to it.
type SecretCipher interface {
	NewVaultKey(string, string, time.Time) (WrappedVaultKey, error)
	EncryptSecret(WrappedVaultKey, string, string, string, string, string, []byte) (EncryptedSecretValue, error)
	DecryptSecret(WrappedVaultKey, string, string, string, string, string, EncryptedSecretValue) ([]byte, error)
}

// VaultKeyRewrapper re-encrypts an existing vault data key under the active
// deployment root key without ever returning the unwrapped key.
type VaultKeyRewrapper interface {
	ActiveRootKeyID() string
	RewrapVaultKey(string, string, WrappedVaultKey) (WrappedVaultKey, error)
}
