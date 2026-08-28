package authz_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/authz"
	"github.com/araihu/moco/internal/core/ports"
)

func TestPolicyReloaderLoadsAndAppliesCommittedSnapshots(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	repository := &reloadRepository{states: []ports.AuthorizationState{{Policies: []ports.AuthorizationPolicy{
		{Subject: "reader", Domain: "*", Path: "/api/v1", Method: "GET"},
	}}, {Policies: []ports.AuthorizationPolicy{
		{Subject: "reader", Domain: "*", Path: "/api/v1/tenants", Method: "GET"},
	}}}}
	bus := authz.NewMemoryPolicyChangesBus()
	reloader, err := authz.NewPolicyReloader(authorizer, repository, bus)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErrors := make(chan error, 1)
	go func() { runErrors <- reloader.Run(ctx) }()
	waitForLoad(t, repository)
	waitForDecision(t, authorizer, "/api/v1", true)
	repository.advance()
	if err := bus.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	waitForLoad(t, repository)
	waitForDecision(t, authorizer, "/api/v1", false)
	waitForDecision(t, authorizer, "/api/v1/tenants", true)
	cancel()
	select {
	case <-runErrors:
	case <-time.After(time.Second):
		t.Fatal("reloader did not stop after context cancellation")
	}
}

func TestPolicyReloaderKeepsPreviousPolicyWhenReloadFails(t *testing.T) {
	t.Parallel()
	authorizer, err := authz.NewStaticAuthorizer(nil, []authz.Policy{{Subject: "reader", Domain: "*", Path: "/api/v1", Method: "GET"}})
	if err != nil {
		t.Fatal(err)
	}
	repository := &reloadRepository{states: []ports.AuthorizationState{{Policies: []ports.AuthorizationPolicy{{Subject: "reader", Domain: "*", Path: "/api/v1", Method: "GET"}}}, {Policies: []ports.AuthorizationPolicy{{Subject: "reader", Domain: "*", Path: "/api/v1/unsafe?query", Method: "GET"}}}}}
	bus := authz.NewMemoryPolicyChangesBus()
	reloader, err := authz.NewPolicyReloader(authorizer, repository, bus)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErrors := make(chan error, 1)
	go func() { runErrors <- reloader.Run(ctx) }()
	waitForLoad(t, repository)
	repository.advance()
	if err := bus.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-runErrors:
		if err == nil {
			t.Fatal("invalid reload unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("invalid reload was not reported")
	}
	allowed, err := authorizer.Authorize(context.Background(), "reader", "*", "/api/v1", "GET")
	if err != nil || !allowed {
		t.Fatalf("previous policy was not retained: allowed=%t err=%v", allowed, err)
	}
}

type reloadRepository struct {
	mu     sync.Mutex
	states []ports.AuthorizationState
	index  int
	loads  chan struct{}
}

func (repository *reloadRepository) LoadAuthorization(context.Context) (ports.AuthorizationState, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.loads == nil {
		repository.loads = make(chan struct{}, 8)
	}
	state := repository.states[repository.index]
	repository.loads <- struct{}{}
	return state, nil
}

func (repository *reloadRepository) ReplaceAuthorization(context.Context, ports.AuthorizationState) error {
	return errors.New("not implemented in reload test")
}

func (repository *reloadRepository) advance() {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if repository.index+1 < len(repository.states) {
		repository.index++
	}
}

func waitForLoad(t *testing.T, repository *reloadRepository) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		repository.mu.Lock()
		loads := repository.loads
		repository.mu.Unlock()
		if loads == nil {
			select {
			case <-deadline:
				t.Fatal("repository was not loaded")
			default:
			}
			continue
		}
		select {
		case <-loads:
			return
		case <-deadline:
			t.Fatal("repository was not loaded")
		}
	}
}

func waitForDecision(t *testing.T, authorizer *authz.StaticAuthorizer, resource string, want bool) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		allowed, err := authorizer.Authorize(context.Background(), "reader", "*", resource, "GET")
		if err == nil && allowed == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatalf("authorization for %s = %t, %v; want %t", resource, allowed, err, want)
		}
	}
}
