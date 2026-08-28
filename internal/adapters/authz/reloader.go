package authz

import (
	"context"
	"fmt"

	"github.com/araihu/moco/internal/core/ports"
)

// PolicyReloader refreshes an in-memory authorizer from the authoritative
// repository whenever a bus signal arrives.
type PolicyReloader struct {
	authorizer *StaticAuthorizer
	repository ports.AuthorizationRepository
	bus        ports.PolicyChangesBus
}

// NewPolicyReloader validates the dependencies for a reload loop.
func NewPolicyReloader(authorizer *StaticAuthorizer, repository ports.AuthorizationRepository, bus ports.PolicyChangesBus) (*PolicyReloader, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("authorizer is required")
	}
	if repository == nil {
		return nil, fmt.Errorf("authorization repository is required")
	}
	if bus == nil {
		return nil, fmt.Errorf("policy changes bus is required")
	}
	return &PolicyReloader{authorizer: authorizer, repository: repository, bus: bus}, nil
}

// Run loads the authoritative state once, then reloads it for each change
// signal. Existing policy remains active if a candidate load or validation
// fails; the returned error lets the composition root surface the incident.
func (r *PolicyReloader) Run(ctx context.Context) error {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	changes, err := r.bus.Subscribe(runContext)
	if err != nil {
		return fmt.Errorf("subscribe to policy changes: %w", err)
	}
	if err := r.reload(runContext); err != nil {
		return err
	}
	for {
		select {
		case <-runContext.Done():
			return runContext.Err()
		case _, ok := <-changes:
			if !ok {
				return nil
			}
			if err := r.reload(runContext); err != nil {
				return err
			}
		}
	}
}

func (r *PolicyReloader) reload(ctx context.Context) error {
	state, err := r.repository.LoadAuthorization(ctx)
	if err != nil {
		return fmt.Errorf("load authorization snapshot: %w", err)
	}
	if err := r.authorizer.Reload(state.RoleBindings, state.Policies); err != nil {
		return fmt.Errorf("reload authorization snapshot: %w", err)
	}
	return nil
}
