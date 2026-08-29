package authz

import (
	"context"
	"fmt"
	"time"

	"github.com/araihu/moco/internal/core/ports"
)

// PolicyReloader refreshes an in-memory authorizer from the authoritative
// repository after local bus signals and when a newer persisted revision is
// observed by polling. This lets separate processes converge without sharing
// an in-process bus.
type PolicyReloader struct {
	authorizer *StaticAuthorizer
	repository ports.AuthorizationRepository
	bus        ports.PolicyChangesBus
	pollEvery  time.Duration
}

const defaultPolicyPollInterval = time.Second

// NewPolicyReloader validates the dependencies for a reload loop.
func NewPolicyReloader(authorizer *StaticAuthorizer, repository ports.AuthorizationRepository, bus ports.PolicyChangesBus) (*PolicyReloader, error) {
	return NewPolicyReloaderWithInterval(authorizer, repository, bus, defaultPolicyPollInterval)
}

// NewPolicyReloaderWithInterval is useful for deployments that need a tighter
// convergence bound or tests that need deterministic, fast polling. The
// repository remains authoritative; the bus is only an optimization for local
// writers, so changes made by another process are still discovered by polling.
func NewPolicyReloaderWithInterval(authorizer *StaticAuthorizer, repository ports.AuthorizationRepository, bus ports.PolicyChangesBus, pollEvery time.Duration) (*PolicyReloader, error) {
	if authorizer == nil {
		return nil, fmt.Errorf("authorizer is required")
	}
	if repository == nil {
		return nil, fmt.Errorf("authorization repository is required")
	}
	if bus == nil {
		return nil, fmt.Errorf("policy changes bus is required")
	}
	if pollEvery <= 0 {
		return nil, fmt.Errorf("policy poll interval must be positive")
	}
	return &PolicyReloader{authorizer: authorizer, repository: repository, bus: bus, pollEvery: pollEvery}, nil
}

// Run loads the authoritative state once, then reloads it for each change
// signal or newer persisted revision. Existing policy remains active if a
// candidate load or validation fails; the returned error lets the composition
// root surface the incident.
func (r *PolicyReloader) Run(ctx context.Context) error {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()
	changes, err := r.bus.Subscribe(runContext)
	if err != nil {
		return fmt.Errorf("subscribe to policy changes: %w", err)
	}
	state, err := r.reload(runContext)
	if err != nil {
		return err
	}
	revision := state.Revision
	ticker := time.NewTicker(r.pollEvery)
	defer ticker.Stop()
	for {
		select {
		case <-runContext.Done():
			return runContext.Err()
		case _, ok := <-changes:
			if !ok {
				return nil
			}
			state, err := r.reload(runContext)
			if err != nil {
				return err
			}
			revision = state.Revision
		case <-ticker.C:
			state, err := r.repository.LoadAuthorization(runContext)
			if err != nil {
				return fmt.Errorf("load authorization snapshot: %w", err)
			}
			if state.Revision == revision {
				continue
			}
			if err := r.apply(state); err != nil {
				return err
			}
			revision = state.Revision
		}
	}
}

func (r *PolicyReloader) reload(ctx context.Context) (ports.AuthorizationState, error) {
	state, err := r.repository.LoadAuthorization(ctx)
	if err != nil {
		return ports.AuthorizationState{}, fmt.Errorf("load authorization snapshot: %w", err)
	}
	if err := r.apply(state); err != nil {
		return ports.AuthorizationState{}, err
	}
	return state, nil
}

func (r *PolicyReloader) apply(state ports.AuthorizationState) error {
	if err := r.authorizer.Reload(state.RoleBindings, state.Policies); err != nil {
		return fmt.Errorf("reload authorization snapshot: %w", err)
	}
	return nil
}
