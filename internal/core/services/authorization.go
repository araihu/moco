package services

import (
	"context"
	"errors"
	"fmt"

	"github.com/araihu/moco/internal/core/ports"
)

// AuthorizationPolicyService coordinates durable policy snapshots and their
// invalidation signal. The repository must commit before Publish is called.
type AuthorizationPolicyService struct {
	repository ports.AuthorizationRepository
	bus        ports.PolicyChangesBus
}

// NewAuthorizationPolicyService constructs the policy persistence boundary.
func NewAuthorizationPolicyService(repository ports.AuthorizationRepository, bus ports.PolicyChangesBus) (*AuthorizationPolicyService, error) {
	if repository == nil {
		return nil, errors.New("authorization repository is required")
	}
	if bus == nil {
		return nil, errors.New("policy changes bus is required")
	}
	return &AuthorizationPolicyService{repository: repository, bus: bus}, nil
}

// ReplaceAuthorization commits a complete snapshot and then broadcasts one
// coalesced invalidation signal. A publish failure means persistence succeeded
// but another instance may need an out-of-band reload.
func (s *AuthorizationPolicyService) ReplaceAuthorization(ctx context.Context, state ports.AuthorizationState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.repository.ReplaceAuthorization(ctx, state); err != nil {
		return fmt.Errorf("replace authorization snapshot: %w", err)
	}
	if err := s.bus.Publish(ctx); err != nil {
		return fmt.Errorf("publish authorization change: %w", err)
	}
	return nil
}
