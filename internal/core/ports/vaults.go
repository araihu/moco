package ports

import (
	"context"
	"time"

	"github.com/araihu/moco/internal/core/domain"
)

// VaultConflictError identifies a tenant-scoped conflicting vault.
type VaultConflictError struct {
	ResourceID string
}

func (e *VaultConflictError) Error() string { return "vault uniqueness conflict" }

// CreateVaultCommand is the atomic persistence input for a vault and its
// optional idempotency record.
type CreateVaultCommand struct {
	Vault                domain.Vault
	PrincipalID          string
	IdempotencyKey       string
	RequestHash          string
	ResponseETag         string
	IdempotencyExpiresAt time.Time
}

// CreateVaultResult contains either a new vault or its stored replay.
type CreateVaultResult struct {
	Vault    domain.Vault
	ETag     string
	Replayed bool
}

// ListVaultsQuery selects one page within a tenant-scoped sequence snapshot.
type ListVaultsQuery struct {
	TenantID         string
	AfterSequence    int64
	SnapshotSequence int64
	Name             *string
	ExternalID       *string
	PageSize         int
}

// VaultRepository persists tenant-scoped vault aggregates.
type VaultRepository interface {
	CreateVault(context.Context, CreateVaultCommand) (CreateVaultResult, error)
	GetVault(context.Context, string, string) (domain.Vault, error)
	MaxVaultSequence(context.Context, string) (int64, error)
	ListVaults(context.Context, ListVaultsQuery) ([]domain.Vault, error)
	UpdateVault(context.Context, string, string, domain.VaultUpdate, *int64, time.Time) (domain.Vault, error)
	DeleteVault(context.Context, string, string, *int64, bool) error
}
