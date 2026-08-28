package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/araihu/moco/internal/core/ports"
	"github.com/araihu/moco/internal/core/services"
)

func TestAuthorizationPolicyServiceCommitsBeforePublishing(t *testing.T) {
	t.Parallel()
	order := []string{}
	repository := fakeAuthorizationRepository{replace: func(ports.AuthorizationState) error {
		order = append(order, "commit")
		return nil
	}}
	bus := fakePolicyChangesBus{publish: func() error {
		order = append(order, "publish")
		return nil
	}}
	service, err := services.NewAuthorizationPolicyService(&repository, &bus)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReplaceAuthorization(context.Background(), ports.AuthorizationState{}); err != nil {
		t.Fatalf("replace authorization: %v", err)
	}
	if len(order) != 2 || order[0] != "commit" || order[1] != "publish" {
		t.Fatalf("operation order = %v, want [commit publish]", order)
	}
}

func TestAuthorizationPolicyServiceDoesNotPublishWhenCommitFails(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("commit failed")
	repository := fakeAuthorizationRepository{replace: func(ports.AuthorizationState) error { return wantErr }}
	bus := fakePolicyChangesBus{}
	service, err := services.NewAuthorizationPolicyService(&repository, &bus)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReplaceAuthorization(context.Background(), ports.AuthorizationState{}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if bus.published {
		t.Fatal("publish ran after failed commit")
	}
}

func TestAuthorizationPolicyServiceReportsPublishFailureAfterCommit(t *testing.T) {
	t.Parallel()
	wantErr := errors.New("publish failed")
	repository := fakeAuthorizationRepository{}
	bus := fakePolicyChangesBus{publish: func() error { return wantErr }}
	service, err := services.NewAuthorizationPolicyService(&repository, &bus)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ReplaceAuthorization(context.Background(), ports.AuthorizationState{}); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !repository.replaced {
		t.Fatal("repository was not committed before publish failure")
	}
}

type fakeAuthorizationRepository struct {
	replace  func(ports.AuthorizationState) error
	replaced bool
}

func (repository *fakeAuthorizationRepository) LoadAuthorization(context.Context) (ports.AuthorizationState, error) {
	return ports.AuthorizationState{}, nil
}

func (repository *fakeAuthorizationRepository) ReplaceAuthorization(_ context.Context, state ports.AuthorizationState) error {
	repository.replaced = true
	if repository.replace == nil {
		return nil
	}
	return repository.replace(state)
}

type fakePolicyChangesBus struct {
	publish   func() error
	published bool
}

func (bus *fakePolicyChangesBus) Publish(context.Context) error {
	bus.published = true
	if bus.publish == nil {
		return nil
	}
	return bus.publish()
}

func (*fakePolicyChangesBus) Subscribe(context.Context) (<-chan struct{}, error) {
	return nil, nil
}
