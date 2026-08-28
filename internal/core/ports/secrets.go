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
)

// WrappedVaultKey is a random vault data key encrypted under a deployment root
// key. Key material is always wrapped before it reaches a repository.
type WrappedVaultKey struct {
	RootKeyID  string
	Salt       []byte
	Ciphertext []byte
	CreatedAt  time.Time
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
	CreateVaultKey(context.Context, string, string, WrappedVaultKey) (WrappedVaultKey, error)
	PutSecret(context.Context, PutSecretCommand) (PutSecretResult, error)
	GetSecret(context.Context, string, string, string) (StoredSecret, error)
	GetSecretMetadata(context.Context, string, string, string) (domain.SecretMetadata, error)
	MaxSecretSequence(context.Context, string, string) (int64, error)
	ListSecretMetadata(context.Context, ListSecretsQuery) ([]domain.SecretMetadata, error)
	DeleteSecret(context.Context, string, string, string, *int64) error
}

// SecretCipher performs the envelope-encryption operations used by the core
// service without exposing deployment key material to it.
type SecretCipher interface {
	NewVaultKey(string, string, time.Time) (WrappedVaultKey, error)
	EncryptSecret(WrappedVaultKey, string, string, string, string, string, []byte) (EncryptedSecretValue, error)
	DecryptSecret(WrappedVaultKey, string, string, string, string, string, EncryptedSecretValue) ([]byte, error)
}
