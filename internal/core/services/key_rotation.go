package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/araihu/moco/internal/core/ports"
)

const (
	DefaultVaultKeyRotationPageSize = 50
	MaxVaultKeyRotationPageSize     = 200
)

// VaultKeyRotationRequest describes one bounded root-key rewrap batch.
// afterTenantID and afterVaultID form an exclusive keyset checkpoint and must
// either both be empty or both be present.
type VaultKeyRotationRequest struct {
	AfterTenantID string
	AfterVaultID  string
	Limit         int
}

// VaultKeyRotationResult reports one batch without exposing key material.
type VaultKeyRotationResult struct {
	Scanned            int
	Rewrapped          int
	Skipped            int
	HasMore            bool
	NextTenantID       *string
	NextVaultID        *string
	ActiveRootKeyID    string
	ActiveRootKeyEpoch int64
	RemainingOldKeys   int64
	Complete           bool
}

// VaultKeyRotationStatus reports the current shared root-key state and the
// number of persisted vault keys that still use an older era. Counts are
// point-in-time diagnostics, not a lock or a snapshot.
type VaultKeyRotationStatus struct {
	ActiveRootKeyID    string
	ActiveRootKeyEpoch int64
	RemainingOldKeys   int64
	Complete           bool
}

// VaultKeyRotationService coordinates bounded, retry-safe root-key rewraps.
type VaultKeyRotationService struct {
	repository ports.VaultKeyRotationRepository
	rewrapper  ports.VaultKeyRewrapper
	keyState   ports.EncryptionKeyStateReader
}

// VaultKeyRotationServiceOptions supplies the shared epoch reader used to
// fence stale deployment processes. It is optional for isolated unit tests.
type VaultKeyRotationServiceOptions struct {
	KeyState ports.EncryptionKeyStateReader
}

// NewVaultKeyRotationService constructs the online root-key rotation service.
func NewVaultKeyRotationService(repository ports.VaultKeyRotationRepository, rewrapper ports.VaultKeyRewrapper, options ...VaultKeyRotationServiceOptions) (*VaultKeyRotationService, error) {
	if repository == nil {
		return nil, errors.New("vault key rotation repository is required")
	}
	if rewrapper == nil {
		return nil, errors.New("vault key rewrapper is required")
	}
	if rewrapper.ActiveRootKeyID() == "" {
		return nil, errors.New("active root key ID is required")
	}
	if len(options) > 1 {
		return nil, errors.New("at most one vault key rotation options value is allowed")
	}
	var keyState ports.EncryptionKeyStateReader
	if len(options) == 1 {
		keyState = options[0].KeyState
	}
	return &VaultKeyRotationService{repository: repository, rewrapper: rewrapper, keyState: keyState}, nil
}

// Rotate scans one keyset page and rewraps keys that do not use the active
// root key. A false replacement result means another worker already won the
// compare-and-swap and is counted as skipped; callers can safely retry the
// same checkpoint.
func (s *VaultKeyRotationService) Rotate(ctx context.Context, request VaultKeyRotationRequest) (VaultKeyRotationResult, error) {
	if err := validateVaultKeyRotationRequest(request); err != nil {
		return VaultKeyRotationResult{}, err
	}
	state, err := s.currentState(ctx)
	if err != nil {
		return VaultKeyRotationResult{}, err
	}
	if request.Limit == 0 {
		request.Limit = DefaultVaultKeyRotationPageSize
	}
	rows, err := s.repository.ListVaultKeys(ctx, ports.ListVaultKeysQuery{
		AfterTenantID: request.AfterTenantID,
		AfterVaultID:  request.AfterVaultID,
		PageSize:      request.Limit + 1,
	})
	if err != nil {
		return VaultKeyRotationResult{}, fmt.Errorf("list vault keys for rotation: %w", err)
	}
	hasMore := len(rows) > request.Limit
	if hasMore {
		rows = rows[:request.Limit]
	}
	result := VaultKeyRotationResult{
		Scanned:            len(rows),
		HasMore:            hasMore,
		ActiveRootKeyID:    s.rewrapper.ActiveRootKeyID(),
		ActiveRootKeyEpoch: state.Epoch,
	}
	var expectedState *ports.EncryptionKeyState
	if s.keyState != nil {
		expectedState = &state
	}
	if hasMore {
		last := rows[len(rows)-1]
		result.NextTenantID = stringPointer(last.TenantID)
		result.NextVaultID = stringPointer(last.VaultID)
	}
	for _, row := range rows {
		if row.Key.RootKeyID == result.ActiveRootKeyID {
			result.Skipped++
			continue
		}
		replacement, err := s.rewrapper.RewrapVaultKey(row.TenantID, row.VaultID, row.Key)
		if err != nil {
			return result, fmt.Errorf("rewrap vault key: %w", err)
		}
		changed, err := s.repository.ReplaceVaultKey(ctx, ports.ReplaceVaultKeyCommand{
			TenantID:         row.TenantID,
			VaultID:          row.VaultID,
			Expected:         row.Key,
			Replace:          replacement,
			ExpectedKeyState: expectedState,
		})
		if err != nil {
			return result, fmt.Errorf("persist rewrapped vault key: %w", err)
		}
		if changed {
			result.Rewrapped++
		} else {
			result.Skipped++
		}
	}
	remaining, err := s.repository.CountVaultKeysNotUsingRootKey(ctx, result.ActiveRootKeyID)
	if err != nil {
		return result, fmt.Errorf("count vault keys pending rotation: %w", err)
	}
	result.RemainingOldKeys = remaining
	result.Complete = remaining == 0
	return result, nil
}

// Status reads the authoritative shared key state and counts vault keys that
// do not use its active era. Unlike Rotate, it does not require this process's
// configured rewrapper to already match the shared state; that makes the
// read-only status useful while a stale process is fencing its writes.
func (s *VaultKeyRotationService) Status(ctx context.Context) (VaultKeyRotationStatus, error) {
	if err := ctx.Err(); err != nil {
		return VaultKeyRotationStatus{}, err
	}
	state := ports.EncryptionKeyState{ActiveRootKeyID: s.rewrapper.ActiveRootKeyID()}
	if s.keyState != nil {
		var err error
		state, err = s.keyState.CurrentEncryptionKeyState(ctx)
		if err != nil {
			return VaultKeyRotationStatus{}, fmt.Errorf("read encryption key state: %w", err)
		}
		if state.Epoch < 1 || state.ActiveRootKeyID == "" {
			return VaultKeyRotationStatus{}, ports.ErrEncryptionKeyStateConflict
		}
	}
	remaining, err := s.repository.CountVaultKeysNotUsingRootKey(ctx, state.ActiveRootKeyID)
	if err != nil {
		return VaultKeyRotationStatus{}, fmt.Errorf("count vault keys pending rotation: %w", err)
	}
	return VaultKeyRotationStatus{
		ActiveRootKeyID:    state.ActiveRootKeyID,
		ActiveRootKeyEpoch: state.Epoch,
		RemainingOldKeys:   remaining,
		Complete:           remaining == 0,
	}, nil
}

func (s *VaultKeyRotationService) currentState(ctx context.Context) (ports.EncryptionKeyState, error) {
	state := ports.EncryptionKeyState{ActiveRootKeyID: s.rewrapper.ActiveRootKeyID()}
	if s.keyState == nil {
		return state, nil
	}
	state, err := s.keyState.CurrentEncryptionKeyState(ctx)
	if err != nil {
		return ports.EncryptionKeyState{}, fmt.Errorf("read encryption key state: %w", err)
	}
	if state.Epoch < 1 || state.ActiveRootKeyID != s.rewrapper.ActiveRootKeyID() {
		return ports.EncryptionKeyState{}, ports.ErrEncryptionKeyStateConflict
	}
	return state, nil
}

func validateVaultKeyRotationRequest(request VaultKeyRotationRequest) error {
	if (request.AfterTenantID == "") != (request.AfterVaultID == "") {
		return errors.New("afterTenantId and afterVaultId must be supplied together")
	}
	if len(request.AfterTenantID) > 128 || len(request.AfterVaultID) > 128 {
		return errors.New("rotation checkpoint identifiers must contain at most 128 bytes")
	}
	if request.Limit < 0 || request.Limit > MaxVaultKeyRotationPageSize {
		return fmt.Errorf("rotation limit must be between 1 and %d", MaxVaultKeyRotationPageSize)
	}
	return nil
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
