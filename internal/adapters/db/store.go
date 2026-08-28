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

// DeleteVault removes one vault. It is empty until the secret slice lands.
func (s *Store) DeleteVault(ctx context.Context, tenantID, id string, expectedRevision *int64, cascade bool) error {
	_ = cascade
	var rows int64
	var err error
	if expectedRevision == nil {
		rows, err = s.queries.DeleteVault(ctx, sqlc.DeleteVaultParams{TenantID: tenantID, ID: id})
	} else {
		rows, err = s.queries.DeleteVaultIfRevision(ctx, sqlc.DeleteVaultIfRevisionParams{
			TenantID: tenantID, ID: id, ExpectedRevision: *expectedRevision,
		})
	}
	if err != nil {
		return fmt.Errorf("delete vault: %w", err)
	}
	if rows == 1 {
		return nil
	}
	if expectedRevision != nil {
		_, getErr := s.queries.GetVault(ctx, sqlc.GetVaultParams{TenantID: tenantID, ID: id})
		if getErr == nil {
			return ports.ErrTenantPrecondition
		}
		if !errors.Is(getErr, sql.ErrNoRows) {
			return fmt.Errorf("resolve vault delete precondition: %w", getErr)
		}
	}
	return ports.ErrVaultNotFound
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
