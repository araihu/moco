package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"time"

	"github.com/araihu/moco/db/migrations"
	"github.com/araihu/moco/internal/adapters/db/sqlc"
	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
	"github.com/golang-migrate/migrate/v4"
	migratesqlite "github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

const (
	createTenantOperation = "createTenant"
	createVaultOperation  = "createVault"
	databaseTimeFormat    = "2006-01-02T15:04:05.000000000Z"
)

// Store is the sqlc-backed SQLite tenant repository.
type Store struct {
	database *sql.DB
	queries  *sqlc.Queries
}

var _ ports.AuthorizationRepository = (*Store)(nil)

// Open opens a SQLite file, applies embedded migrations, and verifies it.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	if err := migrateDatabase(dsn, path); err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("ping SQLite database: %w", err)
	}
	return &Store{database: database, queries: sqlc.New(database)}, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error { return s.database.Close() }

// Ping verifies that the database remains available.
func (s *Store) Ping(ctx context.Context) error { return s.database.PingContext(ctx) }

// LoadAuthorization reads the complete authoritative policy snapshot in a
// deterministic order for reproducible Casbin reloads.
func (s *Store) LoadAuthorization(ctx context.Context) (ports.AuthorizationState, error) {
	bindings, err := s.queries.ListAuthorizationRoleBindings(ctx)
	if err != nil {
		return ports.AuthorizationState{}, fmt.Errorf("list authorization role bindings: %w", err)
	}
	policies, err := s.queries.ListAuthorizationPolicies(ctx)
	if err != nil {
		return ports.AuthorizationState{}, fmt.Errorf("list authorization policies: %w", err)
	}
	state := ports.AuthorizationState{
		RoleBindings: make([]ports.AuthorizationRoleBinding, 0, len(bindings)),
		Policies:     make([]ports.AuthorizationPolicy, 0, len(policies)),
	}
	for _, binding := range bindings {
		state.RoleBindings = append(state.RoleBindings, ports.AuthorizationRoleBinding{
			Principal: binding.PrincipalID,
			Role:      binding.Role,
			Domain:    binding.Domain,
		})
	}
	for _, policy := range policies {
		state.Policies = append(state.Policies, ports.AuthorizationPolicy{
			Subject: policy.Subject,
			Domain:  policy.Domain,
			Path:    policy.Path,
			Method:  policy.Method,
		})
	}
	return state, nil
}

// ReplaceAuthorization atomically replaces the complete policy snapshot.
// Publication of a change signal is deliberately owned by the application
// service so callers can guarantee commit-before-publish ordering.
func (s *Store) ReplaceAuthorization(ctx context.Context, state ports.AuthorizationState) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin authorization replacement: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	if err := queries.DeleteAuthorizationPolicies(ctx); err != nil {
		return fmt.Errorf("clear authorization policies: %w", err)
	}
	if err := queries.DeleteAuthorizationRoleBindings(ctx); err != nil {
		return fmt.Errorf("clear authorization role bindings: %w", err)
	}
	for index, policy := range state.Policies {
		if err := queries.InsertAuthorizationPolicy(ctx, sqlc.InsertAuthorizationPolicyParams{
			Subject: policy.Subject, Domain: policy.Domain, Path: policy.Path, Method: policy.Method,
		}); err != nil {
			return fmt.Errorf("insert authorization policy %d: %w", index, err)
		}
	}
	for index, binding := range state.RoleBindings {
		if err := queries.InsertAuthorizationRoleBinding(ctx, sqlc.InsertAuthorizationRoleBindingParams{
			PrincipalID: binding.Principal, Role: binding.Role, Domain: binding.Domain,
		}); err != nil {
			return fmt.Errorf("insert authorization role binding %d: %w", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit authorization replacement: %w", err)
	}
	return nil
}

// CreateTenant atomically stores a tenant and its optional idempotency result.
func (s *Store) CreateTenant(ctx context.Context, command ports.CreateTenantCommand) (ports.CreateTenantResult, error) {
	if command.IdempotencyKey != "" {
		if err := s.queries.DeleteExpiredIdempotencyRecords(ctx, formatDatabaseTime(command.Tenant.CreatedAt)); err != nil {
			return ports.CreateTenantResult{}, fmt.Errorf("expire idempotency records: %w", err)
		}
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("begin tenant creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)

	if command.IdempotencyKey != "" {
		result, claimed, err := claimIdempotency(ctx, queries, command)
		if err != nil {
			return ports.CreateTenantResult{}, err
		}
		if !claimed {
			return result, nil
		}
	}
	labelsJSON, err := json.Marshal(command.Tenant.Labels)
	if err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("encode tenant labels: %w", err)
	}

	row, err := queries.InsertTenant(ctx, sqlc.InsertTenantParams{
		ID:          command.Tenant.ID,
		Name:        command.Tenant.Name,
		Description: nullableString(command.Tenant.Description),
		ExternalID:  nullableString(command.Tenant.ExternalID),
		LabelsJson:  string(labelsJSON),
		Revision:    command.Tenant.Revision,
		CreatedAt:   formatDatabaseTime(command.Tenant.CreatedAt),
		UpdatedAt:   formatDatabaseTime(command.Tenant.UpdatedAt),
	})
	if err != nil {
		return ports.CreateTenantResult{}, mapTenantConflict(ctx, queries, command.Tenant.Name, command.Tenant.ExternalID, err)
	}
	tenant, err := tenantFromRow(row)
	if err != nil {
		return ports.CreateTenantResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("commit tenant creation: %w", err)
	}
	return ports.CreateTenantResult{Tenant: tenant, ETag: command.ResponseETag}, nil
}

// GetTenant retrieves one tenant by stable ID.
func (s *Store) GetTenant(ctx context.Context, id string) (domain.Tenant, error) {
	row, err := s.queries.GetTenant(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Tenant{}, ports.ErrTenantNotFound
	}
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("get tenant: %w", err)
	}
	return tenantFromRow(row)
}

// MaxTenantSequence returns the immutable upper bound for a list snapshot.
func (s *Store) MaxTenantSequence(ctx context.Context) (int64, error) {
	sequence, err := s.queries.MaxTenantSequence(ctx)
	if err != nil {
		return 0, fmt.Errorf("get maximum tenant sequence: %w", err)
	}
	return sequence, nil
}

// ListTenants returns an ordered page bounded by immutable insertion sequence.
func (s *Store) ListTenants(ctx context.Context, query ports.ListTenantsQuery) ([]domain.Tenant, error) {
	rows, err := s.queries.ListTenantsPage(ctx, sqlc.ListTenantsPageParams{
		AfterSequence:    query.AfterSequence,
		SnapshotSequence: query.SnapshotSequence,
		Name:             optionalArgument(query.Name),
		ExternalID:       optionalArgument(query.ExternalID),
		PageSize:         int64(query.PageSize),
	})
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	tenants := make([]domain.Tenant, 0, len(rows))
	for _, row := range rows {
		tenant, err := tenantFromRow(row)
		if err != nil {
			return nil, err
		}
		tenants = append(tenants, tenant)
	}
	return tenants, nil
}

// UpdateTenant replaces mutable fields, optionally at one exact revision.
func (s *Store) UpdateTenant(ctx context.Context, id string, input domain.TenantUpdate, expectedRevision *int64, updatedAt time.Time) (domain.Tenant, error) {
	labelsJSON, err := json.Marshal(input.Labels)
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("encode tenant labels: %w", err)
	}
	var row sqlc.Tenant
	if expectedRevision == nil {
		row, err = s.queries.UpdateTenant(ctx, sqlc.UpdateTenantParams{
			Name:        input.Name,
			Description: nullableString(input.Description),
			LabelsJson:  string(labelsJSON),
			UpdatedAt:   formatDatabaseTime(updatedAt),
			ID:          id,
		})
	} else {
		row, err = s.queries.UpdateTenantIfRevision(ctx, sqlc.UpdateTenantIfRevisionParams{
			Name:             input.Name,
			Description:      nullableString(input.Description),
			LabelsJson:       string(labelsJSON),
			UpdatedAt:        formatDatabaseTime(updatedAt),
			ID:               id,
			ExpectedRevision: *expectedRevision,
		})
	}
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision != nil {
			_, getErr := s.queries.GetTenant(ctx, id)
			if getErr == nil {
				return domain.Tenant{}, ports.ErrTenantPrecondition
			}
			if !errors.Is(getErr, sql.ErrNoRows) {
				return domain.Tenant{}, fmt.Errorf("resolve tenant update precondition: %w", getErr)
			}
		}
		return domain.Tenant{}, ports.ErrTenantNotFound
	}
	if err != nil {
		return domain.Tenant{}, mapTenantConflict(ctx, s.queries, input.Name, nil, err)
	}
	return tenantFromRow(row)
}

// DeleteTenant removes one tenant, optionally at one exact revision.
func (s *Store) DeleteTenant(ctx context.Context, id string, expectedRevision *int64, cascade bool) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tenant deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	var rows int64
	switch {
	case cascade && expectedRevision == nil:
		rows, err = queries.DeleteTenant(ctx, id)
	case cascade:
		rows, err = queries.DeleteTenantIfRevision(ctx, sqlc.DeleteTenantIfRevisionParams{
			ID: id, ExpectedRevision: *expectedRevision,
		})
	case expectedRevision == nil:
		rows, err = queries.DeleteTenantIfEmpty(ctx, id)
	default:
		rows, err = queries.DeleteTenantIfRevisionAndEmpty(ctx, sqlc.DeleteTenantIfRevisionAndEmptyParams{
			ID: id, ExpectedRevision: *expectedRevision,
		})
	}
	if err != nil {
		return fmt.Errorf("delete tenant: %w", err)
	}
	if rows == 1 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit tenant deletion: %w", err)
		}
		return nil
	}
	current, getErr := queries.GetTenant(ctx, id)
	if errors.Is(getErr, sql.ErrNoRows) {
		return ports.ErrTenantNotFound
	}
	if getErr != nil {
		return fmt.Errorf("resolve tenant deletion: %w", getErr)
	}
	if expectedRevision != nil && current.Revision != *expectedRevision {
		return ports.ErrTenantPrecondition
	}
	children, err := queries.CountTenantVaults(ctx, id)
	if err != nil {
		return fmt.Errorf("count tenant vaults: %w", err)
	}
	if children > 0 {
		return ports.ErrResourceHasChildren
	}
	return errors.New("tenant deletion affected no rows")
}

// CreateVault atomically stores a vault and its optional idempotency result.
func (s *Store) CreateVault(ctx context.Context, command ports.CreateVaultCommand) (ports.CreateVaultResult, error) {
	if command.IdempotencyKey != "" {
		if err := s.queries.DeleteExpiredIdempotencyRecords(ctx, formatDatabaseTime(command.Vault.CreatedAt)); err != nil {
			return ports.CreateVaultResult{}, fmt.Errorf("expire idempotency records: %w", err)
		}
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("begin vault creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	if command.IdempotencyKey != "" {
		result, claimed, err := claimVaultIdempotency(ctx, queries, command)
		if err != nil {
			return ports.CreateVaultResult{}, err
		}
		if !claimed {
			return result, nil
		}
	}
	if _, err := queries.GetTenant(ctx, command.Vault.TenantID); errors.Is(err, sql.ErrNoRows) {
		return ports.CreateVaultResult{}, ports.ErrTenantNotFound
	} else if err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("get vault tenant: %w", err)
	}
	labelsJSON, err := json.Marshal(command.Vault.Labels)
	if err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("encode vault labels: %w", err)
	}
	row, err := queries.InsertVault(ctx, sqlc.InsertVaultParams{
		ID: command.Vault.ID, TenantID: command.Vault.TenantID,
		Name: command.Vault.Name, Description: nullableString(command.Vault.Description),
		ExternalID: nullableString(command.Vault.ExternalID), LabelsJson: string(labelsJSON),
		Revision:  command.Vault.Revision,
		CreatedAt: formatDatabaseTime(command.Vault.CreatedAt), UpdatedAt: formatDatabaseTime(command.Vault.UpdatedAt),
	})
	if err != nil {
		return ports.CreateVaultResult{}, mapVaultConflict(ctx, queries, command.Vault.TenantID, command.Vault.Name, command.Vault.ExternalID, err)
	}
	vault, err := vaultFromRow(row)
	if err != nil {
		return ports.CreateVaultResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("commit vault creation: %w", err)
	}
	return ports.CreateVaultResult{Vault: vault, ETag: command.ResponseETag}, nil
}

// GetVault retrieves one vault within its tenant.
func (s *Store) GetVault(ctx context.Context, tenantID, id string) (domain.Vault, error) {
	row, err := s.queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Vault{}, ports.ErrVaultNotFound
	}
	if err != nil {
		return domain.Vault{}, fmt.Errorf("get vault: %w", err)
	}
	return vaultFromRow(row)
}

// MaxVaultSequence returns a snapshot upper bound or a missing-tenant error.
func (s *Store) MaxVaultSequence(ctx context.Context, tenantID string) (int64, error) {
	bound, err := s.queries.VaultSnapshotUpperBound(ctx, tenantID)
	if err != nil {
		return 0, fmt.Errorf("get maximum vault sequence: %w", err)
	}
	if bound.TenantExists == 0 {
		return 0, ports.ErrTenantNotFound
	}
	return bound.MaxSequence, nil
}

// ListVaults returns one ordered page within a tenant snapshot.
func (s *Store) ListVaults(ctx context.Context, query ports.ListVaultsQuery) ([]domain.Vault, error) {
	tx, err := s.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin vault list: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	bound, err := queries.VaultSnapshotUpperBound(ctx, query.TenantID)
	if err != nil {
		return nil, fmt.Errorf("verify vault tenant: %w", err)
	}
	if bound.TenantExists == 0 {
		return nil, ports.ErrTenantNotFound
	}
	rows, err := queries.ListVaultsPage(ctx, sqlc.ListVaultsPageParams{
		TenantID: query.TenantID, AfterSequence: query.AfterSequence,
		SnapshotSequence: query.SnapshotSequence, Name: optionalArgument(query.Name),
		ExternalID: optionalArgument(query.ExternalID), PageSize: int64(query.PageSize),
	})
	if err != nil {
		return nil, fmt.Errorf("list vaults: %w", err)
	}
	vaults := make([]domain.Vault, 0, len(rows))
	for _, row := range rows {
		vault, err := vaultFromRow(row)
		if err != nil {
			return nil, err
		}
		vaults = append(vaults, vault)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit vault list snapshot: %w", err)
	}
	return vaults, nil
}

// UpdateVault replaces mutable vault fields, optionally at one revision.
func (s *Store) UpdateVault(ctx context.Context, tenantID, id string, input domain.VaultUpdate, expectedRevision *int64, updatedAt time.Time) (domain.Vault, error) {
	labelsJSON, err := json.Marshal(input.Labels)
	if err != nil {
		return domain.Vault{}, fmt.Errorf("encode vault labels: %w", err)
	}
	var row sqlc.Vault
	if expectedRevision == nil {
		row, err = s.queries.UpdateVault(ctx, sqlc.UpdateVaultParams{
			Name: input.Name, Description: nullableString(input.Description), LabelsJson: string(labelsJSON),
			UpdatedAt: formatDatabaseTime(updatedAt), TenantID: tenantID, ID: id,
		})
	} else {
		row, err = s.queries.UpdateVaultIfRevision(ctx, sqlc.UpdateVaultIfRevisionParams{
			Name: input.Name, Description: nullableString(input.Description), LabelsJson: string(labelsJSON),
			UpdatedAt: formatDatabaseTime(updatedAt), TenantID: tenantID, ID: id,
			ExpectedRevision: *expectedRevision,
		})
	}
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision != nil {
			_, getErr := s.queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: id})
			if getErr == nil {
				return domain.Vault{}, ports.ErrTenantPrecondition
			}
			if !errors.Is(getErr, sql.ErrNoRows) {
				return domain.Vault{}, fmt.Errorf("resolve vault update precondition: %w", getErr)
			}
		}
		return domain.Vault{}, ports.ErrVaultNotFound
	}
	if err != nil {
		return domain.Vault{}, mapVaultConflict(ctx, s.queries, tenantID, input.Name, nil, err)
	}
	return vaultFromRow(row)
}

// DeleteVault removes one vault, refusing implicit deletion of child secrets.
func (s *Store) DeleteVault(ctx context.Context, tenantID, id string, expectedRevision *int64, cascade bool) error {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin vault deletion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	var rows int64
	switch {
	case cascade && expectedRevision == nil:
		rows, err = queries.DeleteVault(ctx, sqlc.DeleteVaultParams{TenantID: tenantID, ID: id})
	case cascade:
		rows, err = queries.DeleteVaultIfRevision(ctx, sqlc.DeleteVaultIfRevisionParams{
			TenantID: tenantID, ID: id, ExpectedRevision: *expectedRevision,
		})
	case expectedRevision == nil:
		rows, err = queries.DeleteVaultIfEmpty(ctx, sqlc.DeleteVaultIfEmptyParams{TenantID: tenantID, ID: id})
	default:
		rows, err = queries.DeleteVaultIfRevisionAndEmpty(ctx, sqlc.DeleteVaultIfRevisionAndEmptyParams{
			TenantID: tenantID, ID: id, ExpectedRevision: *expectedRevision,
		})
	}
	if err != nil {
		return fmt.Errorf("delete vault: %w", err)
	}
	if rows == 1 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit vault deletion: %w", err)
		}
		return nil
	}
	current, getErr := queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: id})
	if errors.Is(getErr, sql.ErrNoRows) {
		return ports.ErrVaultNotFound
	}
	if getErr != nil {
		return fmt.Errorf("resolve vault deletion: %w", getErr)
	}
	if expectedRevision != nil && current.Revision != *expectedRevision {
		return ports.ErrTenantPrecondition
	}
	children, err := queries.CountVaultSecrets(ctx, sqlc.CountVaultSecretsParams{TenantID: tenantID, VaultID: id})
	if err != nil {
		return fmt.Errorf("count vault secrets: %w", err)
	}
	if children > 0 {
		return ports.ErrResourceHasChildren
	}
	return errors.New("vault deletion affected no rows")
}

// GetVaultKey retrieves one wrapped vault data key.
func (s *Store) GetVaultKey(ctx context.Context, tenantID, vaultID string) (ports.WrappedVaultKey, error) {
	row, err := s.queries.GetVaultKey(ctx, sqlc.GetVaultKeyParams{TenantID: tenantID, VaultID: vaultID})
	if errors.Is(err, sql.ErrNoRows) {
		if _, vaultErr := s.queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: vaultID}); errors.Is(vaultErr, sql.ErrNoRows) {
			return ports.WrappedVaultKey{}, ports.ErrVaultNotFound
		} else if vaultErr != nil {
			return ports.WrappedVaultKey{}, fmt.Errorf("resolve missing vault key: %w", vaultErr)
		}
		return ports.WrappedVaultKey{}, ports.ErrVaultKeyNotFound
	}
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("get vault key: %w", err)
	}
	return vaultKeyFromRow(row)
}

// CreateVaultKey atomically installs a wrapped key or returns the concurrent winner.
func (s *Store) CreateVaultKey(ctx context.Context, tenantID, vaultID string, candidate ports.WrappedVaultKey) (ports.WrappedVaultKey, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("begin vault key creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	if _, err := queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: vaultID}); errors.Is(err, sql.ErrNoRows) {
		return ports.WrappedVaultKey{}, ports.ErrVaultNotFound
	} else if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("get keyed vault: %w", err)
	}
	if _, err := queries.InsertVaultKey(ctx, sqlc.InsertVaultKeyParams{
		TenantID: tenantID, VaultID: vaultID, RootKeyID: candidate.RootKeyID,
		Salt: candidate.Salt, WrappedKey: candidate.Ciphertext,
		CreatedAt: formatDatabaseTime(candidate.CreatedAt),
	}); err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("insert vault key: %w", err)
	}
	row, err := queries.GetVaultKey(ctx, sqlc.GetVaultKeyParams{TenantID: tenantID, VaultID: vaultID})
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("read installed vault key: %w", err)
	}
	key, err := vaultKeyFromRow(row)
	if err != nil {
		return ports.WrappedVaultKey{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("commit vault key creation: %w", err)
	}
	return key, nil
}

// PutSecret atomically creates, replaces, or replays one encrypted value.
func (s *Store) PutSecret(ctx context.Context, command ports.PutSecretCommand) (ports.PutSecretResult, error) {
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return ports.PutSecretResult{}, fmt.Errorf("begin secret write: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	if _, err := queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: command.TenantID, ID: command.VaultID}); errors.Is(err, sql.ErrNoRows) {
		return ports.PutSecretResult{}, ports.ErrVaultNotFound
	} else if err != nil {
		return ports.PutSecretResult{}, fmt.Errorf("get secret vault: %w", err)
	}
	row, err := putSecretRow(ctx, queries, command)
	if errors.Is(err, sql.ErrNoRows) {
		currentRow, currentErr := queries.GetSecretMetadata(ctx, sqlc.GetSecretMetadataParams{
			TenantID: command.TenantID, VaultID: command.VaultID, Path: command.Path,
		})
		if errors.Is(currentErr, sql.ErrNoRows) {
			return ports.PutSecretResult{}, ports.ErrSecretNotFound
		}
		if currentErr != nil {
			return ports.PutSecretResult{}, fmt.Errorf("resolve secret write: %w", currentErr)
		}
		current, convertErr := secretMetadataFromGetRow(currentRow)
		if convertErr != nil {
			return ports.PutSecretResult{}, convertErr
		}
		if command.CreateOnly || (command.ExpectedVersion != nil && current.Version != *command.ExpectedVersion) {
			return ports.PutSecretResult{}, ports.ErrSecretPrecondition
		}
		if current.Digest != command.Digest || current.ContentType != command.ContentType {
			return ports.PutSecretResult{}, errors.New("secret write affected no rows")
		}
		if err := tx.Commit(); err != nil {
			return ports.PutSecretResult{}, fmt.Errorf("commit idempotent secret write: %w", err)
		}
		return ports.PutSecretResult{Metadata: current}, nil
	}
	if err != nil {
		return ports.PutSecretResult{}, fmt.Errorf("persist encrypted secret: %w", err)
	}
	metadata, err := secretMetadataFromSecret(row)
	if err != nil {
		return ports.PutSecretResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return ports.PutSecretResult{}, fmt.Errorf("commit secret write: %w", err)
	}
	return ports.PutSecretResult{Metadata: metadata, Created: metadata.Version == 1}, nil
}

// GetSecret retrieves encrypted value material and its wrapped vault key.
func (s *Store) GetSecret(ctx context.Context, tenantID, vaultID, path string) (ports.StoredSecret, error) {
	row, err := s.queries.GetSecretRecord(ctx, sqlc.GetSecretRecordParams{TenantID: tenantID, VaultID: vaultID, Path: path})
	if errors.Is(err, sql.ErrNoRows) {
		if _, metadataErr := s.queries.GetSecretMetadata(ctx, sqlc.GetSecretMetadataParams{TenantID: tenantID, VaultID: vaultID, Path: path}); metadataErr == nil {
			return ports.StoredSecret{}, errors.New("persisted secret has no vault key")
		} else if !errors.Is(metadataErr, sql.ErrNoRows) {
			return ports.StoredSecret{}, fmt.Errorf("resolve missing secret record: %w", metadataErr)
		}
		return ports.StoredSecret{}, s.resolveMissingSecret(ctx, s.queries, tenantID, vaultID)
	}
	if err != nil {
		return ports.StoredSecret{}, fmt.Errorf("get encrypted secret: %w", err)
	}
	return storedSecretFromRow(row)
}

// GetSecretMetadata retrieves metadata without selecting ciphertext.
func (s *Store) GetSecretMetadata(ctx context.Context, tenantID, vaultID, path string) (domain.SecretMetadata, error) {
	row, err := s.queries.GetSecretMetadata(ctx, sqlc.GetSecretMetadataParams{TenantID: tenantID, VaultID: vaultID, Path: path})
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SecretMetadata{}, s.resolveMissingSecret(ctx, s.queries, tenantID, vaultID)
	}
	if err != nil {
		return domain.SecretMetadata{}, fmt.Errorf("get secret metadata: %w", err)
	}
	return secretMetadataFromGetRow(row)
}

// MaxSecretSequence returns a list snapshot upper bound or a missing-vault error.
func (s *Store) MaxSecretSequence(ctx context.Context, tenantID, vaultID string) (int64, error) {
	bound, err := s.queries.SecretSnapshotUpperBound(ctx, sqlc.SecretSnapshotUpperBoundParams{TenantID: tenantID, VaultID: vaultID})
	if err != nil {
		return 0, fmt.Errorf("get maximum secret sequence: %w", err)
	}
	if bound.VaultExists == 0 {
		return 0, ports.ErrVaultNotFound
	}
	return bound.MaxSequence, nil
}

// ListSecretMetadata returns a metadata-only page within one vault snapshot.
func (s *Store) ListSecretMetadata(ctx context.Context, query ports.ListSecretsQuery) ([]domain.SecretMetadata, error) {
	tx, err := s.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin secret list: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	queries := s.queries.WithTx(tx)
	bound, err := queries.SecretSnapshotUpperBound(ctx, sqlc.SecretSnapshotUpperBoundParams{TenantID: query.TenantID, VaultID: query.VaultID})
	if err != nil {
		return nil, fmt.Errorf("verify secret vault: %w", err)
	}
	if bound.VaultExists == 0 {
		return nil, ports.ErrVaultNotFound
	}
	rows, err := queries.ListSecretMetadataPage(ctx, sqlc.ListSecretMetadataPageParams{
		TenantID: query.TenantID, VaultID: query.VaultID,
		AfterSequence: query.AfterSequence, SnapshotSequence: query.SnapshotSequence,
		Prefix: optionalArgument(query.Prefix), PageSize: int64(query.PageSize),
	})
	if err != nil {
		return nil, fmt.Errorf("list secret metadata: %w", err)
	}
	items := make([]domain.SecretMetadata, 0, len(rows))
	for _, row := range rows {
		metadata, err := secretMetadataFromListRow(row)
		if err != nil {
			return nil, err
		}
		items = append(items, metadata)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit secret list snapshot: %w", err)
	}
	return items, nil
}

// DeleteSecret removes one path, optionally at one exact version.
func (s *Store) DeleteSecret(ctx context.Context, tenantID, vaultID, path string, expectedVersion *int64) error {
	var rows int64
	var err error
	if expectedVersion == nil {
		rows, err = s.queries.DeleteSecret(ctx, sqlc.DeleteSecretParams{TenantID: tenantID, VaultID: vaultID, Path: path})
	} else {
		rows, err = s.queries.DeleteSecretIfVersion(ctx, sqlc.DeleteSecretIfVersionParams{
			TenantID: tenantID, VaultID: vaultID, Path: path, ExpectedVersion: *expectedVersion,
		})
	}
	if err != nil {
		return fmt.Errorf("delete secret: %w", err)
	}
	if rows == 1 {
		return nil
	}
	if expectedVersion != nil {
		if _, getErr := s.queries.GetSecretMetadata(ctx, sqlc.GetSecretMetadataParams{TenantID: tenantID, VaultID: vaultID, Path: path}); getErr == nil {
			return ports.ErrSecretPrecondition
		} else if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("resolve secret delete precondition: %w", getErr)
		}
	}
	return s.resolveMissingSecret(ctx, s.queries, tenantID, vaultID)
}

func putSecretRow(ctx context.Context, queries *sqlc.Queries, command ports.PutSecretCommand) (sqlc.Secret, error) {
	if command.CreateOnly {
		return queries.InsertSecret(ctx, sqlc.InsertSecretParams{
			TenantID: command.TenantID, VaultID: command.VaultID, Path: command.Path,
			Salt: command.Value.Salt, Ciphertext: command.Value.Ciphertext,
			Digest: command.Digest, ContentType: command.ContentType,
			CreatedAt: formatDatabaseTime(command.UpdatedAt), UpdatedAt: formatDatabaseTime(command.UpdatedAt),
		})
	}
	if command.ExpectedVersion != nil {
		return queries.UpdateSecretIfVersion(ctx, sqlc.UpdateSecretIfVersionParams{
			Salt: command.Value.Salt, Ciphertext: command.Value.Ciphertext,
			Digest: command.Digest, ContentType: command.ContentType,
			UpdatedAt: formatDatabaseTime(command.UpdatedAt), TenantID: command.TenantID,
			VaultID: command.VaultID, Path: command.Path, ExpectedVersion: *command.ExpectedVersion,
		})
	}
	return queries.UpsertSecret(ctx, sqlc.UpsertSecretParams{
		TenantID: command.TenantID, VaultID: command.VaultID, Path: command.Path,
		Salt: command.Value.Salt, Ciphertext: command.Value.Ciphertext,
		Digest: command.Digest, ContentType: command.ContentType,
		CreatedAt: formatDatabaseTime(command.UpdatedAt), UpdatedAt: formatDatabaseTime(command.UpdatedAt),
	})
}

func claimIdempotency(ctx context.Context, queries *sqlc.Queries, command ports.CreateTenantCommand) (ports.CreateTenantResult, bool, error) {
	key := sqlc.GetIdempotencyRecordParams{
		PrincipalID: command.PrincipalID, Operation: createTenantOperation, IdempotencyKey: command.IdempotencyKey,
	}
	if record, err := queries.GetIdempotencyRecord(ctx, key); err == nil {
		result, err := replayIdempotency(record, command.RequestHash)
		return result, false, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ports.CreateTenantResult{}, false, fmt.Errorf("read idempotency record: %w", err)
	}

	responseJSON, err := json.Marshal(command.Tenant)
	if err != nil {
		return ports.CreateTenantResult{}, false, fmt.Errorf("encode idempotency response: %w", err)
	}
	rows, err := queries.InsertIdempotencyRecord(ctx, sqlc.InsertIdempotencyRecordParams{
		PrincipalID:    key.PrincipalID,
		Operation:      key.Operation,
		IdempotencyKey: key.IdempotencyKey,
		RequestHash:    command.RequestHash,
		StatusCode:     201,
		ResourceID:     command.Tenant.ID,
		ResponseJson:   responseJSON,
		ResponseEtag:   command.ResponseETag,
		CreatedAt:      formatDatabaseTime(command.Tenant.CreatedAt),
		ExpiresAt:      formatDatabaseTime(command.IdempotencyExpiresAt),
	})
	if err != nil {
		return ports.CreateTenantResult{}, false, fmt.Errorf("claim idempotency key: %w", err)
	}
	if rows == 1 {
		return ports.CreateTenantResult{}, true, nil
	}
	record, err := queries.GetIdempotencyRecord(ctx, key)
	if err != nil {
		return ports.CreateTenantResult{}, false, fmt.Errorf("read concurrently claimed idempotency record: %w", err)
	}
	result, err := replayIdempotency(record, command.RequestHash)
	return result, false, err
}

func replayIdempotency(record sqlc.IdempotencyRecord, requestHash string) (ports.CreateTenantResult, error) {
	if record.RequestHash != requestHash {
		return ports.CreateTenantResult{}, ports.ErrIdempotencyConflict
	}
	var tenant domain.Tenant
	if err := json.Unmarshal(record.ResponseJson, &tenant); err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("decode idempotency response: %w", err)
	}
	tenant.Labels = domain.CloneLabels(tenant.Labels)
	return ports.CreateTenantResult{Tenant: tenant, ETag: record.ResponseEtag, Replayed: true}, nil
}

func claimVaultIdempotency(ctx context.Context, queries *sqlc.Queries, command ports.CreateVaultCommand) (ports.CreateVaultResult, bool, error) {
	key := sqlc.GetIdempotencyRecordParams{
		PrincipalID: command.PrincipalID, Operation: createVaultOperation, IdempotencyKey: command.IdempotencyKey,
	}
	if record, err := queries.GetIdempotencyRecord(ctx, key); err == nil {
		result, err := replayVaultIdempotency(record, command.RequestHash)
		return result, false, err
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ports.CreateVaultResult{}, false, fmt.Errorf("read vault idempotency record: %w", err)
	}
	responseJSON, err := json.Marshal(command.Vault)
	if err != nil {
		return ports.CreateVaultResult{}, false, fmt.Errorf("encode vault idempotency response: %w", err)
	}
	rows, err := queries.InsertIdempotencyRecord(ctx, sqlc.InsertIdempotencyRecordParams{
		PrincipalID: key.PrincipalID, Operation: key.Operation, IdempotencyKey: key.IdempotencyKey,
		RequestHash: command.RequestHash, StatusCode: 201, ResourceID: command.Vault.ID,
		ResponseJson: responseJSON, ResponseEtag: command.ResponseETag,
		CreatedAt: formatDatabaseTime(command.Vault.CreatedAt), ExpiresAt: formatDatabaseTime(command.IdempotencyExpiresAt),
	})
	if err != nil {
		return ports.CreateVaultResult{}, false, fmt.Errorf("claim vault idempotency key: %w", err)
	}
	if rows == 1 {
		return ports.CreateVaultResult{}, true, nil
	}
	record, err := queries.GetIdempotencyRecord(ctx, key)
	if err != nil {
		return ports.CreateVaultResult{}, false, fmt.Errorf("read concurrently claimed vault idempotency record: %w", err)
	}
	result, err := replayVaultIdempotency(record, command.RequestHash)
	return result, false, err
}

func replayVaultIdempotency(record sqlc.IdempotencyRecord, requestHash string) (ports.CreateVaultResult, error) {
	if record.RequestHash != requestHash {
		return ports.CreateVaultResult{}, ports.ErrIdempotencyConflict
	}
	var vault domain.Vault
	if err := json.Unmarshal(record.ResponseJson, &vault); err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("decode vault idempotency response: %w", err)
	}
	vault.Labels = domain.CloneLabels(vault.Labels)
	return ports.CreateVaultResult{Vault: vault, ETag: record.ResponseEtag, Replayed: true}, nil
}

func tenantFromRow(row sqlc.Tenant) (domain.Tenant, error) {
	createdAt, err := time.Parse(databaseTimeFormat, row.CreatedAt)
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("parse tenant creation time: %w", err)
	}
	updatedAt, err := time.Parse(databaseTimeFormat, row.UpdatedAt)
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("parse tenant update time: %w", err)
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(row.LabelsJson), &labels); err != nil {
		return domain.Tenant{}, fmt.Errorf("decode tenant labels: %w", err)
	}
	return domain.Tenant{
		Sequence:    row.Sequence,
		ID:          row.ID,
		Name:        row.Name,
		Description: pointerFromNull(row.Description),
		ExternalID:  pointerFromNull(row.ExternalID),
		Labels:      labels,
		Revision:    row.Revision,
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
	}, nil
}

func vaultFromRow(row sqlc.Vault) (domain.Vault, error) {
	createdAt, err := time.Parse(databaseTimeFormat, row.CreatedAt)
	if err != nil {
		return domain.Vault{}, fmt.Errorf("parse vault creation time: %w", err)
	}
	updatedAt, err := time.Parse(databaseTimeFormat, row.UpdatedAt)
	if err != nil {
		return domain.Vault{}, fmt.Errorf("parse vault update time: %w", err)
	}
	labels := map[string]string{}
	if err := json.Unmarshal([]byte(row.LabelsJson), &labels); err != nil {
		return domain.Vault{}, fmt.Errorf("decode vault labels: %w", err)
	}
	return domain.Vault{
		Sequence: row.Sequence, ID: row.ID, TenantID: row.TenantID, Name: row.Name,
		Description: pointerFromNull(row.Description), ExternalID: pointerFromNull(row.ExternalID),
		Labels: labels, Revision: row.Revision, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func vaultKeyFromRow(row sqlc.GetVaultKeyRow) (ports.WrappedVaultKey, error) {
	createdAt, err := time.Parse(databaseTimeFormat, row.CreatedAt)
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("parse vault key creation time: %w", err)
	}
	return ports.WrappedVaultKey{
		RootKeyID:  row.RootKeyID,
		Salt:       append([]byte(nil), row.Salt...),
		Ciphertext: append([]byte(nil), row.WrappedKey...),
		CreatedAt:  createdAt,
	}, nil
}

func storedSecretFromRow(row sqlc.GetSecretRecordRow) (ports.StoredSecret, error) {
	metadata, err := secretMetadata(
		row.Sequence, row.TenantID, row.VaultID, row.Path, row.Digest,
		row.ContentType, row.Version, row.CreatedAt, row.UpdatedAt,
	)
	if err != nil {
		return ports.StoredSecret{}, err
	}
	keyCreatedAt, err := time.Parse(databaseTimeFormat, row.KeyCreatedAt)
	if err != nil {
		return ports.StoredSecret{}, fmt.Errorf("parse vault key creation time: %w", err)
	}
	return ports.StoredSecret{
		Metadata: metadata,
		Value: ports.EncryptedSecretValue{
			Salt:       append([]byte(nil), row.SecretSalt...),
			Ciphertext: append([]byte(nil), row.Ciphertext...),
		},
		VaultKey: ports.WrappedVaultKey{
			RootKeyID:  row.RootKeyID,
			Salt:       append([]byte(nil), row.KeySalt...),
			Ciphertext: append([]byte(nil), row.WrappedKey...),
			CreatedAt:  keyCreatedAt,
		},
	}, nil
}

func secretMetadataFromSecret(row sqlc.Secret) (domain.SecretMetadata, error) {
	return secretMetadata(
		row.Sequence, row.TenantID, row.VaultID, row.Path, row.Digest,
		row.ContentType, row.Version, row.CreatedAt, row.UpdatedAt,
	)
}

func secretMetadataFromGetRow(row sqlc.GetSecretMetadataRow) (domain.SecretMetadata, error) {
	return secretMetadata(
		row.Sequence, row.TenantID, row.VaultID, row.Path, row.Digest,
		row.ContentType, row.Version, row.CreatedAt, row.UpdatedAt,
	)
}

func secretMetadataFromListRow(row sqlc.ListSecretMetadataPageRow) (domain.SecretMetadata, error) {
	return secretMetadata(
		row.Sequence, row.TenantID, row.VaultID, row.Path, row.Digest,
		row.ContentType, row.Version, row.CreatedAt, row.UpdatedAt,
	)
}

func secretMetadata(
	sequence int64,
	tenantID, vaultID, path, digest, contentType string,
	version int64,
	createdAtValue, updatedAtValue string,
) (domain.SecretMetadata, error) {
	createdAt, err := time.Parse(databaseTimeFormat, createdAtValue)
	if err != nil {
		return domain.SecretMetadata{}, fmt.Errorf("parse secret creation time: %w", err)
	}
	updatedAt, err := time.Parse(databaseTimeFormat, updatedAtValue)
	if err != nil {
		return domain.SecretMetadata{}, fmt.Errorf("parse secret update time: %w", err)
	}
	return domain.SecretMetadata{
		Sequence: sequence, TenantID: tenantID, VaultID: vaultID, Path: path,
		Digest: digest, ContentType: contentType, Version: version,
		CreatedAt: createdAt, UpdatedAt: updatedAt,
	}, nil
}

func (s *Store) resolveMissingSecret(ctx context.Context, queries *sqlc.Queries, tenantID, vaultID string) error {
	_, err := queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: vaultID})
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ErrVaultNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve missing secret scope: %w", err)
	}
	return ports.ErrSecretNotFound
}

func mapTenantConflict(ctx context.Context, queries *sqlc.Queries, name string, externalID *string, original error) error {
	resourceID, err := queries.FindTenantConflict(ctx, sqlc.FindTenantConflictParams{
		Name: name, ExternalID: optionalArgument(externalID),
	})
	if err == nil {
		return &ports.TenantConflictError{ResourceID: resourceID}
	}
	return fmt.Errorf("persist tenant: %w", original)
}

func mapVaultConflict(ctx context.Context, queries *sqlc.Queries, tenantID, name string, externalID *string, original error) error {
	resourceID, err := queries.FindVaultConflict(ctx, sqlc.FindVaultConflictParams{
		TenantID: tenantID, Name: name, ExternalID: optionalArgument(externalID),
	})
	if err == nil {
		return &ports.VaultConflictError{ResourceID: resourceID}
	}
	if _, tenantErr := queries.GetTenant(ctx, tenantID); errors.Is(tenantErr, sql.ErrNoRows) {
		return ports.ErrTenantNotFound
	}
	return fmt.Errorf("persist vault: %w", original)
}

func migrateDatabase(dsn, name string) error {
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return fmt.Errorf("open SQLite database for migrations: %w", err)
	}
	database.SetMaxOpenConns(1)
	sourceDriver, err := iofs.New(migrations.Files, ".")
	if err != nil {
		_ = database.Close()
		return fmt.Errorf("open embedded migrations: %w", err)
	}
	databaseDriver, err := migratesqlite.WithInstance(database, &migratesqlite.Config{DatabaseName: name})
	if err != nil {
		_ = sourceDriver.Close()
		_ = database.Close()
		return fmt.Errorf("initialize SQLite migrator: %w", err)
	}
	migrator, err := migrate.NewWithInstance("iofs", sourceDriver, name, databaseDriver)
	if err != nil {
		_ = sourceDriver.Close()
		_ = database.Close()
		return fmt.Errorf("construct SQLite migrator: %w", err)
	}
	migrationErr := migrator.Up()
	sourceCloseErr, databaseCloseErr := migrator.Close()
	if migrationErr != nil && !errors.Is(migrationErr, migrate.ErrNoChange) {
		return fmt.Errorf("apply SQLite migrations: %w", migrationErr)
	}
	if sourceCloseErr != nil || databaseCloseErr != nil {
		return fmt.Errorf("close SQLite migrator: %w", errors.Join(sourceCloseErr, databaseCloseErr))
	}
	return nil
}

func sqliteDSN(path string) (string, error) {
	if path == "" {
		return "", errors.New("SQLite path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite path: %w", err)
	}
	value := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	query := value.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "journal_mode(WAL)")
	value.RawQuery = query.Encode()
	return value.String(), nil
}

func nullableString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func pointerFromNull(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copy := value.String
	return &copy
}

func optionalArgument(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func formatDatabaseTime(value time.Time) string { return value.UTC().Format(databaseTimeFormat) }
