package ports

import (
	"context"
	"errors"
	"time"

	"github.com/araihu/moco/internal/core/domain"
)

var (
	// ErrTenantNotFound means the tenant ID is absent.
	ErrTenantNotFound = errors.New("tenant not found")
	// ErrTenantPrecondition means the persisted revision changed.
	ErrTenantPrecondition = errors.New("tenant revision precondition failed")
	// ErrIdempotencyConflict means a key was reused with a different request.
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
)

// TenantConflictError identifies a conflicting resource without leaking inputs.
type TenantConflictError struct {
	ResourceID string
}

func (e *TenantConflictError) Error() string { return "tenant uniqueness conflict" }

// CreateTenantCommand is the atomic persistence input for a tenant and its
// optional idempotency record.
type CreateTenantCommand struct {
	Tenant               domain.Tenant
	PrincipalID          string
	IdempotencyKey       string
	RequestHash          string
	ResponseETag         string
	IdempotencyExpiresAt time.Time
}

// CreateTenantResult contains either a new representation or its stored replay.
type CreateTenantResult struct {
	Tenant   domain.Tenant
	ETag     string
	Replayed bool
}

// ListTenantsQuery selects one page within an immutable sequence snapshot.
type ListTenantsQuery struct {
	AfterSequence    int64
	SnapshotSequence int64
	Name             *string
	ExternalID       *string
	PageSize         int
}

// TenantRepository persists tenant aggregates and idempotency records.
type TenantRepository interface {
	CreateTenant(context.Context, CreateTenantCommand) (CreateTenantResult, error)
	GetTenant(context.Context, string) (domain.Tenant, error)
	MaxTenantSequence(context.Context) (int64, error)
	ListTenants(context.Context, ListTenantsQuery) ([]domain.Tenant, error)
	UpdateTenant(context.Context, string, domain.TenantUpdate, *int64, time.Time) (domain.Tenant, error)
	DeleteTenant(context.Context, string, *int64) error
}
