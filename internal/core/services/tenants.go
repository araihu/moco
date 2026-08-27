package services

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
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

const (
	defaultPageSize       = 50
	maxPageSize           = 200
	defaultCursorTTL      = 15 * time.Minute
	defaultIdempotencyTTL = 24 * time.Hour
)

var (
	// ErrInvalidCursor means a cursor is malformed, tampered with, or for other filters.
	ErrInvalidCursor = errors.New("invalid tenant cursor")
	// ErrCursorExpired means a valid cursor's stable snapshot lifetime elapsed.
	ErrCursorExpired = errors.New("tenant cursor expired")
)

// PreconditionError reports the authoritative ETag after a conditional miss.
type PreconditionError struct {
	CurrentETag string
}

func (e *PreconditionError) Error() string { return "tenant ETag precondition failed" }

// TenantServiceOptions supplies secrets and deterministic test seams.
type TenantServiceOptions struct {
	CursorHMACKey  []byte
	Clock          func() time.Time
	NewID          func() (string, error)
	CursorTTL      time.Duration
	IdempotencyTTL time.Duration
}

// TenantService implements tenant lifecycle semantics independently of HTTP and SQL.
type TenantService struct {
	repository     ports.TenantRepository
	cursorHMACKey  []byte
	clock          func() time.Time
	newID          func() (string, error)
	cursorTTL      time.Duration
	idempotencyTTL time.Duration
}

// TenantListRequest describes one list request.
type TenantListRequest struct {
	Limit      int
	Cursor     *string
	Name       *string
	ExternalID *string
}

// TenantListResult is a stable page plus its continuation state.
type TenantListResult struct {
	Items      []domain.Tenant
	HasMore    bool
	NextCursor *string
}

// NewTenantService constructs a tenant service. Cursor keys shorter than 32
// bytes are rejected because they authenticate externally supplied state.
func NewTenantService(repository ports.TenantRepository, options TenantServiceOptions) (*TenantService, error) {
	if repository == nil {
		return nil, errors.New("tenant repository is required")
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
		return nil, errors.New("invalid tenant service lifetime configuration")
	}
	return &TenantService{
		repository:     repository,
		cursorHMACKey:  append([]byte(nil), options.CursorHMACKey...),
		clock:          options.Clock,
		newID:          options.NewID,
		cursorTTL:      options.CursorTTL,
		idempotencyTTL: options.IdempotencyTTL,
	}, nil
}

// Create creates a tenant or replays the original keyed result.
func (s *TenantService) Create(ctx context.Context, principalID string, input domain.TenantCreate, idempotencyKey *string) (ports.CreateTenantResult, error) {
	input.Labels = domain.CloneLabels(input.Labels)
	if err := domain.ValidateTenantCreate(input); err != nil {
		return ports.CreateTenantResult{}, err
	}
	key := ""
	if idempotencyKey != nil {
		key = *idempotencyKey
		if err := domain.ValidateIdempotencyKey(key); err != nil {
			return ports.CreateTenantResult{}, err
		}
		if principalID == "" {
			return ports.CreateTenantResult{}, errors.New("authenticated principal is required for idempotency")
		}
	}

	id, err := s.newID()
	if err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("generate tenant ID: %w", err)
	}
	now := s.clock().UTC()
	tenant := domain.Tenant{
		ID:          id,
		Name:        input.Name,
		Description: cloneString(input.Description),
		ExternalID:  cloneString(input.ExternalID),
		Labels:      domain.CloneLabels(input.Labels),
		Revision:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	hash, err := tenantCreateHash(input)
	if err != nil {
		return ports.CreateTenantResult{}, fmt.Errorf("hash tenant request: %w", err)
	}
	return s.repository.CreateTenant(ctx, ports.CreateTenantCommand{
		Tenant:               tenant,
		PrincipalID:          principalID,
		IdempotencyKey:       key,
		RequestHash:          hash,
		ResponseETag:         TenantETag(tenant),
		IdempotencyExpiresAt: now.Add(s.idempotencyTTL),
	})
}

// Get retrieves one tenant and its current strong ETag.
func (s *TenantService) Get(ctx context.Context, id string) (domain.Tenant, string, error) {
	tenant, err := s.repository.GetTenant(ctx, id)
	if err != nil {
		return domain.Tenant{}, "", err
	}
	return tenant, TenantETag(tenant), nil
}

// List returns one stable snapshot page.
func (s *TenantService) List(ctx context.Context, request TenantListRequest) (TenantListResult, error) {
	if request.Limit == 0 {
		request.Limit = defaultPageSize
	}
	if request.Limit < 1 || request.Limit > maxPageSize {
		return TenantListResult{}, &domain.ValidationError{Violations: []domain.FieldViolation{{
			Field: "limit", Code: "out_of_range", Message: "must be between 1 and 200",
		}}}
	}
	if err := domain.ValidateTenantFilters(request.Name, request.ExternalID); err != nil {
		return TenantListResult{}, err
	}

	state := tenantCursor{
		Version:    1,
		Name:       cloneString(request.Name),
		ExternalID: cloneString(request.ExternalID),
	}
	if request.Cursor == nil {
		snapshot, err := s.repository.MaxTenantSequence(ctx)
		if err != nil {
			return TenantListResult{}, err
		}
		state.SnapshotSequence = snapshot
		state.ExpiresAt = s.clock().UTC().Add(s.cursorTTL).Unix()
	} else {
		if len(*request.Cursor) < 1 || len(*request.Cursor) > 2048 {
			return TenantListResult{}, ErrInvalidCursor
		}
		decoded, err := s.decodeCursor(*request.Cursor)
		if err != nil {
			return TenantListResult{}, err
		}
		if !sameOptionalString(decoded.Name, request.Name) || !sameOptionalString(decoded.ExternalID, request.ExternalID) {
			return TenantListResult{}, ErrInvalidCursor
		}
		state = decoded
	}

	rows, err := s.repository.ListTenants(ctx, ports.ListTenantsQuery{
		AfterSequence:    state.AfterSequence,
		SnapshotSequence: state.SnapshotSequence,
		Name:             request.Name,
		ExternalID:       request.ExternalID,
		PageSize:         request.Limit + 1,
	})
	if err != nil {
		return TenantListResult{}, err
	}
	hasMore := len(rows) > request.Limit
	if hasMore {
		rows = rows[:request.Limit]
	}
	result := TenantListResult{Items: rows, HasMore: hasMore}
	if hasMore {
		state.AfterSequence = rows[len(rows)-1].Sequence
		cursor, err := s.encodeCursor(state)
		if err != nil {
			return TenantListResult{}, err
		}
		result.NextCursor = &cursor
	}
	return result, nil
}

// Update replaces all mutable tenant fields.
func (s *TenantService) Update(ctx context.Context, id string, input domain.TenantUpdate, ifMatch *string) (domain.Tenant, string, error) {
	input.Labels = domain.CloneLabels(input.Labels)
	if err := domain.ValidateTenantUpdate(input); err != nil {
		return domain.Tenant{}, "", err
	}
	expectedRevision, err := s.expectedRevision(ctx, id, ifMatch)
	if err != nil {
		return domain.Tenant{}, "", err
	}
	tenant, err := s.repository.UpdateTenant(ctx, id, input, expectedRevision, s.clock().UTC())
	if errors.Is(err, ports.ErrTenantPrecondition) {
		return domain.Tenant{}, "", s.currentPrecondition(ctx, id)
	}
	if err != nil {
		return domain.Tenant{}, "", err
	}
	return tenant, TenantETag(tenant), nil
}

// Delete removes a tenant. The cascade flag is reserved for the vault slice;
// every tenant is necessarily empty in the current schema.
func (s *TenantService) Delete(ctx context.Context, id string, ifMatch *string, cascade bool) error {
	_ = cascade
	expectedRevision, err := s.expectedRevision(ctx, id, ifMatch)
	if err != nil {
		return err
	}
	err = s.repository.DeleteTenant(ctx, id, expectedRevision)
	if errors.Is(err, ports.ErrTenantPrecondition) {
		return s.currentPrecondition(ctx, id)
	}
	return err
}

// TenantETag returns a strong, opaque validator for one tenant revision.
func TenantETag(tenant domain.Tenant) string {
	return fmt.Sprintf("\"tenant-%s-%d\"", tenant.ID, tenant.Revision)
}

// IfNoneMatch reports whether a GET conditional matches the current entity.
func IfNoneMatch(header, currentETag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == currentETag || strings.TrimPrefix(candidate, "W/") == currentETag {
			return true
		}
	}
	return false
}

func (s *TenantService) expectedRevision(ctx context.Context, id string, ifMatch *string) (*int64, error) {
	if ifMatch == nil {
		return nil, nil
	}
	current, err := s.repository.GetTenant(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ifMatchStrong(*ifMatch, TenantETag(current)) {
		return nil, &PreconditionError{CurrentETag: TenantETag(current)}
	}
	revision := current.Revision
	return &revision, nil
}

func (s *TenantService) currentPrecondition(ctx context.Context, id string) error {
	current, err := s.repository.GetTenant(ctx, id)
	if err != nil {
		return err
	}
	return &PreconditionError{CurrentETag: TenantETag(current)}
}

func ifMatchStrong(header, currentETag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == currentETag {
			return true
		}
	}
	return false
}

type tenantCursor struct {
	Version          int     `json:"v"`
	SnapshotSequence int64   `json:"s"`
	AfterSequence    int64   `json:"a"`
	ExpiresAt        int64   `json:"e"`
	Name             *string `json:"n,omitempty"`
	ExternalID       *string `json:"x,omitempty"`
}

func (s *TenantService) encodeCursor(cursor tenantCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode tenant cursor: %w", err)
	}
	signature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = signature.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

func (s *TenantService) decodeCursor(value string) (tenantCursor, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return tenantCursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return tenantCursor{}, ErrInvalidCursor
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return tenantCursor{}, ErrInvalidCursor
	}
	expectedSignature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = expectedSignature.Write(payload)
	if !hmac.Equal(providedSignature, expectedSignature.Sum(nil)) {
		return tenantCursor{}, ErrInvalidCursor
	}
	var cursor tenantCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.SnapshotSequence < 0 || cursor.AfterSequence < 0 || cursor.AfterSequence > cursor.SnapshotSequence {
		return tenantCursor{}, ErrInvalidCursor
	}
	if s.clock().UTC().Unix() >= cursor.ExpiresAt {
		return tenantCursor{}, ErrCursorExpired
	}
	return cursor, nil
}

func tenantCreateHash(input domain.TenantCreate) (string, error) {
	payload, err := json.Marshal(struct {
		Name        string            `json:"name"`
		Description *string           `json:"description"`
		ExternalID  *string           `json:"externalId"`
		Labels      map[string]string `json:"labels"`
	}{
		Name:        input.Name,
		Description: input.Description,
		ExternalID:  input.ExternalID,
		Labels:      input.Labels,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
