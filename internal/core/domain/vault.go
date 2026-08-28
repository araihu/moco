package domain

import "time"

// Vault is a tenant-scoped container for encrypted secrets.
type Vault struct {
	Sequence    int64             `json:"-"`
	ID          string            `json:"id"`
	TenantID    string            `json:"tenantId"`
	Name        string            `json:"name"`
	Description *string           `json:"description,omitempty"`
	ExternalID  *string           `json:"externalId,omitempty"`
	Labels      map[string]string `json:"labels"`
	Revision    int64             `json:"revision"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// VaultCreate contains caller-controlled fields for vault creation.
type VaultCreate struct {
	Name        string
	Description *string
	ExternalID  *string
	Labels      map[string]string
}

// VaultUpdate contains the complete mutable vault state.
type VaultUpdate struct {
	Name        string
	Description *string
	Labels      map[string]string
}

// ValidateVaultCreate validates a vault creation request.
func ValidateVaultCreate(input VaultCreate) error {
	return ValidateTenantCreate(TenantCreate(input))
}

// ValidateVaultUpdate validates a full vault replacement.
func ValidateVaultUpdate(input VaultUpdate) error {
	return ValidateTenantUpdate(TenantUpdate(input))
}
