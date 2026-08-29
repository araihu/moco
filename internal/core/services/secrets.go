package services

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/araihu/moco/internal/core/domain"
	"github.com/araihu/moco/internal/core/ports"
)

// SecretServiceOptions supplies cursor signing and deterministic test seams.
type SecretServiceOptions struct {
	CursorHMACKey []byte
	Clock         func() time.Time
	CursorTTL     time.Duration
}

// SecretService implements encrypted, tenant- and vault-scoped secret semantics.
type SecretService struct {
	repository    ports.SecretRepository
	cipher        ports.SecretCipher
	cursorHMACKey []byte
	clock         func() time.Time
	cursorTTL     time.Duration
}

// SecretListRequest describes one metadata-only list request.
type SecretListRequest struct {
	TenantID string
	VaultID  string
	Prefix   *string
	Limit    int
	Cursor   *string
}

// SecretListResult is one stable metadata page.
type SecretListResult struct {
	Items      []domain.SecretMetadata
	HasMore    bool
	NextCursor *string
}

// NewSecretService constructs the encrypted secret application service.
func NewSecretService(repository ports.SecretRepository, cipher ports.SecretCipher, options SecretServiceOptions) (*SecretService, error) {
	if repository == nil {
		return nil, errors.New("secret repository is required")
	}
	if cipher == nil {
		return nil, errors.New("secret cipher is required")
	}
	if len(options.CursorHMACKey) < 32 {
		return nil, errors.New("cursor HMAC key must contain at least 32 bytes")
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.CursorTTL == 0 {
		options.CursorTTL = defaultCursorTTL
	}
	if options.CursorTTL < 0 {
		return nil, errors.New("invalid secret cursor lifetime")
	}
	return &SecretService{
		repository:    repository,
		cipher:        cipher,
		cursorHMACKey: append([]byte(nil), options.CursorHMACKey...),
		clock:         options.Clock,
		cursorTTL:     options.CursorTTL,
	}, nil
}

// Put creates or replaces one encrypted secret.
func (s *SecretService) Put(
	ctx context.Context,
	tenantID, vaultID, path string,
	input domain.SecretWrite,
	ifMatch, ifNoneMatch *string,
) (ports.PutSecretResult, string, error) {
	if err := domain.ValidateSecretPath(path); err != nil {
		return ports.PutSecretResult{}, "", err
	}
	contentType, err := domain.ValidateSecretWrite(input)
	if err != nil {
		return ports.PutSecretResult{}, "", err
	}
	createOnly, expectedVersion, err := s.secretWritePrecondition(ctx, tenantID, vaultID, path, ifMatch, ifNoneMatch)
	if err != nil {
		return ports.PutSecretResult{}, "", err
	}
	key, err := s.ensureVaultKey(ctx, tenantID, vaultID)
	if err != nil {
		return ports.PutSecretResult{}, "", err
	}
	digest := secretDigest(input.Value)
	encrypted, err := s.cipher.EncryptSecret(key, tenantID, vaultID, path, digest, contentType, input.Value)
	if err != nil {
		return ports.PutSecretResult{}, "", fmt.Errorf("encrypt secret: %w", err)
	}
	result, err := s.repository.PutSecret(ctx, ports.PutSecretCommand{
		TenantID: tenantID, VaultID: vaultID, Path: path,
		Digest: digest, ContentType: contentType, Value: encrypted,
		UpdatedAt: s.clock().UTC(), CreateOnly: createOnly, ExpectedVersion: expectedVersion,
	})
	if errors.Is(err, ports.ErrSecretPrecondition) {
		return ports.PutSecretResult{}, "", s.currentSecretPrecondition(ctx, tenantID, vaultID, path)
	}
	if err != nil {
		return ports.PutSecretResult{}, "", err
	}
	return result, SecretETag(result.Metadata), nil
}

// Get decrypts one secret value only after its full scope is resolved.
func (s *SecretService) Get(ctx context.Context, tenantID, vaultID, path string) (domain.Secret, string, error) {
	if err := domain.ValidateSecretPath(path); err != nil {
		return domain.Secret{}, "", err
	}
	stored, err := s.repository.GetSecret(ctx, tenantID, vaultID, path)
	if err != nil {
		return domain.Secret{}, "", err
	}
	plaintext, err := s.cipher.DecryptSecret(
		stored.VaultKey, tenantID, vaultID, path,
		stored.Metadata.Digest, stored.Metadata.ContentType, stored.Value,
	)
	if err != nil {
		return domain.Secret{}, "", fmt.Errorf("decrypt secret: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(secretDigest(plaintext)), []byte(stored.Metadata.Digest)) != 1 {
		wipeBytes(plaintext)
		return domain.Secret{}, "", errors.New("decrypted secret digest does not match authenticated metadata")
	}
	return domain.Secret{Metadata: stored.Metadata, Value: plaintext}, SecretETag(stored.Metadata), nil
}

// GetMetadata retrieves one secret without loading its ciphertext or vault key.
func (s *SecretService) GetMetadata(ctx context.Context, tenantID, vaultID, path string) (domain.SecretMetadata, string, error) {
	if err := domain.ValidateSecretPath(path); err != nil {
		return domain.SecretMetadata{}, "", err
	}
	metadata, err := s.repository.GetSecretMetadata(ctx, tenantID, vaultID, path)
	if err != nil {
		return domain.SecretMetadata{}, "", err
	}
	return metadata, SecretETag(metadata), nil
}

// List returns one metadata-only page from a stable insertion snapshot.
func (s *SecretService) List(ctx context.Context, request SecretListRequest) (SecretListResult, error) {
	if request.Limit == 0 {
		request.Limit = defaultPageSize
	}
	if request.Limit < 1 || request.Limit > maxPageSize {
		return SecretListResult{}, &domain.ValidationError{Violations: []domain.FieldViolation{{
			Field: "limit", Code: "out_of_range", Message: "must be between 1 and 200",
		}}}
	}
	if err := domain.ValidateSecretPrefix(request.Prefix); err != nil {
		return SecretListResult{}, err
	}
	state := secretCursor{
		Version: 1, Kind: "secret", TenantID: request.TenantID, VaultID: request.VaultID,
		Prefix: cloneString(request.Prefix),
	}
	if request.Cursor == nil {
		snapshot, err := s.repository.MaxSecretSequence(ctx, request.TenantID, request.VaultID)
		if err != nil {
			return SecretListResult{}, err
		}
		state.SnapshotSequence = snapshot
		state.ExpiresAt = s.clock().UTC().Add(s.cursorTTL).Unix()
	} else {
		if len(*request.Cursor) < 1 || len(*request.Cursor) > 2048 {
			return SecretListResult{}, ErrInvalidCursor
		}
		decoded, err := s.decodeSecretCursor(*request.Cursor)
		if err != nil {
			return SecretListResult{}, err
		}
		if decoded.TenantID != request.TenantID || decoded.VaultID != request.VaultID || !sameOptionalString(decoded.Prefix, request.Prefix) {
			return SecretListResult{}, ErrInvalidCursor
		}
		state = decoded
	}
	rows, err := s.repository.ListSecretMetadata(ctx, ports.ListSecretsQuery{
		TenantID: request.TenantID, VaultID: request.VaultID, Prefix: request.Prefix,
		AfterSequence: state.AfterSequence, SnapshotSequence: state.SnapshotSequence,
		PageSize: request.Limit + 1,
	})
	if err != nil {
		return SecretListResult{}, err
	}
	hasMore := len(rows) > request.Limit
	if hasMore {
		rows = rows[:request.Limit]
	}
	result := SecretListResult{Items: rows, HasMore: hasMore}
	if hasMore {
		state.AfterSequence = rows[len(rows)-1].Sequence
		cursor, err := s.encodeSecretCursor(state)
		if err != nil {
			return SecretListResult{}, err
		}
		result.NextCursor = &cursor
	}
	return result, nil
}

// Delete removes one secret, optionally at an exact ETag.
func (s *SecretService) Delete(ctx context.Context, tenantID, vaultID, path string, ifMatch *string) error {
	if err := domain.ValidateSecretPath(path); err != nil {
		return err
	}
	var expectedVersion *int64
	if ifMatch != nil {
		if *ifMatch == "" {
			return conditionalValidation("If-Match", "must not be empty")
		}
		current, err := s.repository.GetSecretMetadata(ctx, tenantID, vaultID, path)
		if err != nil {
			return err
		}
		if !ifMatchStrong(*ifMatch, SecretETag(current)) {
			return &PreconditionError{CurrentETag: SecretETag(current)}
		}
		version := current.Version
		expectedVersion = &version
	}
	err := s.repository.DeleteSecret(ctx, tenantID, vaultID, path, expectedVersion)
	if errors.Is(err, ports.ErrSecretPrecondition) {
		return s.currentSecretPrecondition(ctx, tenantID, vaultID, path)
	}
	return err
}

// SecretETag returns a strong validator bound to the immutable row sequence,
// vault, path, and version. Including sequence prevents a delete/recreate
// cycle from making an old validator valid for a new secret at the same path.
func SecretETag(metadata domain.SecretMetadata) string {
	scope := sha256.Sum256([]byte(metadata.VaultID + "\x00" + metadata.Path))
	return fmt.Sprintf("\"secret-%s-%d-%d\"", hex.EncodeToString(scope[:16]), metadata.Sequence, metadata.Version)
}

func (s *SecretService) ensureVaultKey(ctx context.Context, tenantID, vaultID string) (ports.WrappedVaultKey, error) {
	key, err := s.repository.GetVaultKey(ctx, tenantID, vaultID)
	if err == nil {
		return key, nil
	}
	if !errors.Is(err, ports.ErrVaultKeyNotFound) {
		return ports.WrappedVaultKey{}, err
	}
	candidate, err := s.cipher.NewVaultKey(tenantID, vaultID, s.clock().UTC())
	if err != nil {
		return ports.WrappedVaultKey{}, fmt.Errorf("generate vault key: %w", err)
	}
	return s.repository.CreateVaultKey(ctx, tenantID, vaultID, candidate)
}

func (s *SecretService) secretWritePrecondition(
	ctx context.Context,
	tenantID, vaultID, path string,
	ifMatch, ifNoneMatch *string,
) (bool, *int64, error) {
	if ifMatch != nil && ifNoneMatch != nil {
		return false, nil, conditionalValidation("If-Match", "must not be sent with If-None-Match")
	}
	if ifNoneMatch != nil {
		if *ifNoneMatch != "*" {
			return false, nil, conditionalValidation("If-None-Match", "must equal '*' for PUT")
		}
		current, err := s.repository.GetSecretMetadata(ctx, tenantID, vaultID, path)
		if err == nil {
			return false, nil, &PreconditionError{CurrentETag: SecretETag(current)}
		}
		if !errors.Is(err, ports.ErrSecretNotFound) {
			return false, nil, err
		}
		return true, nil, nil
	}
	if ifMatch == nil {
		return false, nil, nil
	}
	if *ifMatch == "" {
		return false, nil, conditionalValidation("If-Match", "must not be empty")
	}
	current, err := s.repository.GetSecretMetadata(ctx, tenantID, vaultID, path)
	if err != nil {
		return false, nil, err
	}
	if !ifMatchStrong(*ifMatch, SecretETag(current)) {
		return false, nil, &PreconditionError{CurrentETag: SecretETag(current)}
	}
	version := current.Version
	return false, &version, nil
}

func (s *SecretService) currentSecretPrecondition(ctx context.Context, tenantID, vaultID, path string) error {
	current, err := s.repository.GetSecretMetadata(ctx, tenantID, vaultID, path)
	if err != nil {
		return err
	}
	return &PreconditionError{CurrentETag: SecretETag(current)}
}

func conditionalValidation(field, message string) error {
	return &domain.ValidationError{Violations: []domain.FieldViolation{{
		Field: field, Code: "invalid_condition", Message: message,
	}}}
}

func secretDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type secretCursor struct {
	Version          int     `json:"v"`
	Kind             string  `json:"k"`
	TenantID         string  `json:"t"`
	VaultID          string  `json:"w"`
	SnapshotSequence int64   `json:"s"`
	AfterSequence    int64   `json:"a"`
	ExpiresAt        int64   `json:"e"`
	Prefix           *string `json:"p,omitempty"`
}

func (s *SecretService) encodeSecretCursor(cursor secretCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", fmt.Errorf("encode secret cursor: %w", err)
	}
	signature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = signature.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil)), nil
}

func (s *SecretService) decodeSecretCursor(value string) (secretCursor, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return secretCursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return secretCursor{}, ErrInvalidCursor
	}
	providedSignature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return secretCursor{}, ErrInvalidCursor
	}
	expectedSignature := hmac.New(sha256.New, s.cursorHMACKey)
	_, _ = expectedSignature.Write(payload)
	if !hmac.Equal(providedSignature, expectedSignature.Sum(nil)) {
		return secretCursor{}, ErrInvalidCursor
	}
	var cursor secretCursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Kind != "secret" || cursor.TenantID == "" || cursor.VaultID == "" || cursor.SnapshotSequence < 0 || cursor.AfterSequence < 0 || cursor.AfterSequence > cursor.SnapshotSequence {
		return secretCursor{}, ErrInvalidCursor
	}
	if s.clock().UTC().Unix() >= cursor.ExpiresAt {
		return secretCursor{}, ErrCursorExpired
	}
	return cursor, nil
}

func wipeBytes(value []byte) {
	clear(value)
	runtime.KeepAlive(value)
}
