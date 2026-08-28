package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
)

// VaultServiceOptions supplies secrets and deterministic test seams.
type VaultServiceOptions struct {
	CursorHMACKey  []byte
	Clock          func() time.Time
	NewID          func() (string, error)
	CursorTTL      time.Duration
	IdempotencyTTL time.Duration
}

// VaultService implements tenant-scoped vault lifecycle semantics.
type VaultService struct {
	repository     ports.VaultRepository
	cursorHMACKey  []byte
	clock          func() time.Time
	newID          func() (string, error)
	cursorTTL      time.Duration
	idempotencyTTL time.Duration
}

// VaultListRequest describes one tenant-scoped list request.
type VaultListRequest struct {
	TenantID   string
	Limit      int
	Cursor     *string
	Name       *string
	ExternalID *string
}

// VaultListResult is a stable page plus its continuation state.
type VaultListResult struct {
	Items      []domain.Vault
	HasMore    bool
	NextCursor *string
}

// NewVaultService constructs a vault service.
func NewVaultService(repository ports.VaultRepository, options VaultServiceOptions) (*VaultService, error) {
	if repository == nil {
		return nil, errors.New("vault repository is required")
	}
	if len(options.CursorHMACKey) < 32 {
		return nil, errors.New("cursor HMAC key must contain at least 32 bytes")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.NewID == nil {
		options.NewID = randomUUID
	}
	if options.CursorTTL == 0 {
		options.CursorTTL = defaultCursorTTL
	}
	if options.IdempotencyTTL == 0 {
		options.IdempotencyTTL = defaultIdempotencyTTL
	}
	if options.CursorTTL < 0 || options.IdempotencyTTL < defaultIdempotencyTTL {
		return nil, errors.New("invalid vault service lifetime configuration")
	}
	return &VaultService{
		repository: repository, cursorHMACKey: append([]byte(nil), options.CursorHMACKey...),
		clock: options.Clock, newID: options.NewID,
		cursorTTL: options.CursorTTL, idempotencyTTL: options.IdempotencyTTL,
	}, nil
}

// Create creates a vault or replays the original keyed result.
func (s *VaultService) Create(ctx context.Context, principalID, tenantID string, input domain.VaultCreate, idempotencyKey *string) (ports.CreateVaultResult, error) {
	input.Labels = domain.CloneLabels(input.Labels)
	if err := domain.ValidateVaultCreate(input); err != nil {
		return ports.CreateVaultResult{}, err
	}
	key := ""
	if idempotencyKey != nil {
		key = *idempotencyKey
		if err := domain.ValidateIdempotencyKey(key); err != nil {
			return ports.CreateVaultResult{}, err
		}
		if principalID == "" {
			return ports.CreateVaultResult{}, errors.New("authenticated principal is required for idempotency")
		}
	}
	id, err := s.newID()
	if err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("generate vault ID: %w", err)
	}
	now := s.clock().UTC()
	vault := domain.Vault{
		ID: id, TenantID: tenantID, Name: input.Name,
		Description: cloneString(input.Description), ExternalID: cloneString(input.ExternalID),
		Labels: domain.CloneLabels(input.Labels), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	hash, err := vaultCreateHash(tenantID, input)
	if err != nil {
		return ports.CreateVaultResult{}, fmt.Errorf("hash vault request: %w", err)
	}
	return s.repository.CreateVault(ctx, ports.CreateVaultCommand{
		Vault: vault, PrincipalID: principalID, IdempotencyKey: key,
		RequestHash: hash, ResponseETag: VaultETag(vault),
		IdempotencyExpiresAt: now.Add(s.idempotencyTTL),
	})
}

// Get retrieves one tenant-scoped vault and its ETag.
func (s *VaultService) Get(ctx context.Context, tenantID, id string) (domain.Vault, string, error) {
	vault, err := s.repository.GetVault(ctx, tenantID, id)
	if err != nil {
		return domain.Vault{}, "", err
	}
	return vault, VaultETag(vault), nil
}

// List returns one stable tenant-scoped vault page.
func (s *VaultService) List(ctx context.Context, request VaultListRequest) (VaultListResult, error) {
	if request.Limit == 0 {
		request.Limit = defaultPageSize
	}
	if request.Limit < 1 || request.Limit > maxPageSize {
		return VaultListResult{}, &domain.ValidationError{Violations: []domain.FieldViolation{{
			Field: "limit", Code: "out_of_range", Message: "must be between 1 and 200",
		}}}
	}
	if err := domain.ValidateTenantFilters(request.Name, request.ExternalID); err != nil {
		return VaultListResult{}, err
	}
	state := vaultCursor{
		Version: 1, Kind: "vault", TenantID: request.TenantID,
		Name: cloneString(request.Name), ExternalID: cloneString(request.ExternalID),
	}
	if request.Cursor == nil {
		snapshot, err := s.repository.MaxVaultSequence(ctx, request.TenantID)
		if err != nil {
			return VaultListResult{}, err
		}
		state.SnapshotSequence = snapshot
		state.ExpiresAt = s.clock().UTC().Add(s.cursorTTL).Unix()
	} else {
		if len(*request.Cursor) < 1 || len(*request.Cursor) > 2048 {
			return VaultListResult{}, ErrInvalidCursor
		}
		decoded, err := s.decodeVaultCursor(*request.Cursor)
		if err != nil {
			return VaultListResult{}, err
		}
		if decoded.TenantID != request.TenantID || !sameOptionalString(decoded.Name, request.Name) || !sameOptionalString(decoded.ExternalID, request.ExternalID) {
			return VaultListResult{}, ErrInvalidCursor
		}
		state = decoded
	}
	rows, err := s.repository.ListVaults(ctx, ports.ListVaultsQuery{
		TenantID: request.TenantID, AfterSequence: state.AfterSequence,
		SnapshotSequence: state.SnapshotSequence, Name: request.Name,
		ExternalID: request.ExternalID, PageSize: request.Limit + 1,
	})
	if err != nil {
		return VaultListResult{}, err
	}
	hasMore := len(rows) > request.Limit
	if hasMore {
		rows = rows[:request.Limit]
	}
	result := VaultListResult{Items: rows, HasMore: hasMore}
	if hasMore {
		state.AfterSequence = rows[len(rows)-1].Sequence
		cursor, err := s.encodeVaultCursor(state)
		if err != nil {
			return VaultListResult{}, err
		}
		result.NextCursor = &cursor
	}
	return result, nil
}

// Update replaces all mutable vault fields.
func (s *VaultService) Update(ctx context.Context, tenantID, id string, input domain.VaultUpdate, ifMatch *string) (domain.Vault, string, error) {
	input.Labels = domain.CloneLabels(input.Labels)
	if err := domain.ValidateVaultUpdate(input); err != nil {
		return domain.Vault{}, "", err
	}
	expectedRevision, err := s.expectedVaultRevision(ctx, tenantID, id, ifMatch)
	if err != nil {
		return domain.Vault{}, "", err
	}
	vault, err := s.repository.UpdateVault(ctx, tenantID, id, input, expectedRevision, s.clock().UTC())
	if errors.Is(err, ports.ErrTenantPrecondition) {
		return domain.Vault{}, "", s.currentVaultPrecondition(ctx, tenantID, id)
	}
	if err != nil {
		return domain.Vault{}, "", err
	}
	return vault, VaultETag(vault), nil
}

// Delete removes an empty vault; cascade is reserved for the secret slice.
func (s *VaultService) Delete(ctx context.Context, tenantID, id string, ifMatch *string, cascade bool) error {
	expectedRevision, err := s.expectedVaultRevision(ctx, tenantID, id, ifMatch)
	if err != nil {
		return err
	}
	err = s.repository.DeleteVault(ctx, tenantID, id, expectedRevision, cascade)
	if errors.Is(err, ports.ErrTenantPrecondition) {
		return s.currentVaultPrecondition(ctx, tenantID, id)
	}
	return err
}

// VaultETag returns a strong validator for one vault revision.
func VaultETag(vault domain.Vault) string {
	return fmt.Sprintf("\"vault-%s-%d\"", vault.ID, vault.Revision)
}

func (s *VaultService) expectedVaultRevision(ctx context.Context, tenantID, id string, ifMatch *string) (*int64, error) {
	if ifMatch == nil {
		return nil, nil
	}
	current, err := s.repository.GetVault(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if !ifMatchStrong(*ifMatch, VaultETag(current)) {
		return nil, &PreconditionError{CurrentETag: VaultETag(current)}
	}
	revision := current.Revision
	return &revision, nil
}

func (s *VaultService) currentVaultPrecondition(ctx context.Context, tenantID, id string) error {
	current, err := s.repository.GetVault(ctx, tenantID, id)
	if err != nil {
		return err
	}
	return &PreconditionError{CurrentETag: VaultETag(current)}
}

type vaultCursor struct {
	Version          int     `json:"v"`
	Kind             string  `json:"k"`
	TenantID         string  `json:"t"`
	SnapshotSequence int64   `json:"s"`
	AfterSequence    int64   `json:"a"`
	ExpiresAt        int64   `json:"e"`
	Name             *string `json:"n,omitempty"`
	ExternalID       *string `json:"x,omitempty"`
}

func (s *VaultService) encodeVaultCursor(cursor vaultCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode vault cursor: %w", err)
	}
	signature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = signature.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

func (s *VaultService) decodeVaultCursor(value string) (vaultCursor, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return vaultCursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return vaultCursor{}, ErrInvalidCursor
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return vaultCursor{}, ErrInvalidCursor
	}
	expectedSignature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = expectedSignature.Write(payload)
	if !hmac.Equal(providedSignature, expectedSignature.Sum(nil)) {
		return vaultCursor{}, ErrInvalidCursor
	}
	var cursor vaultCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Kind != "vault" || cursor.TenantID == "" || cursor.SnapshotSequence < 0 || cursor.AfterSequence < 0 || cursor.AfterSequence > cursor.SnapshotSequence {
		return vaultCursor{}, ErrInvalidCursor
	}
	if s.clock().UTC().Unix() >= cursor.ExpiresAt {
		return vaultCursor{}, ErrCursorExpired
	}
	return cursor, nil
}

func vaultCreateHash(tenantID string, input domain.VaultCreate) (string, error) {
	payload, err := json.Marshal(struct {
		TenantID    string            `json:"tenantId"`
		Name        string            `json:"name"`
		Description *string           `json:"description"`
		ExternalID  *string           `json:"externalId"`
		Labels      map[string]string `json:"labels"`
	}{
		TenantID: tenantID, Name: input.Name, Description: input.Description,
		ExternalID: input.ExternalID, Labels: input.Labels,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}
