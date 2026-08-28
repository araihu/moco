package authz_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/araihu/moco/internal/adapters/authz"
)

func TestMemoryPolicyChangesBusBroadcastsCoalescedSignals(t *testing.T) {
	t.Parallel()
	bus := authz.NewMemoryPolicyChangesBus()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes, err := bus.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("policy change was not delivered")
	}
	select {
	case <-changes:
		t.Fatal("duplicate signal was not coalesced")
	default:
	}
	cancel()
	select {
	case _, ok := <-changes:
		if ok {
			t.Fatal("subscription remained open after cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription did not close after cancellation")
	}
}

func TestMemoryPolicyChangesBusRejectsCanceledAndClosedOperations(t *testing.T) {
	t.Parallel()
	bus := authz.NewMemoryPolicyChangesBus()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := bus.Subscribe(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("subscribe error = %v, want context.Canceled", err)
	}
	bus.Close()
	if err := bus.Publish(context.Background()); err == nil {
		t.Fatal("publish on closed bus unexpectedly succeeded")
	}
	if _, err := bus.Subscribe(context.Background()); err == nil {
		t.Fatal("subscribe on closed bus unexpectedly succeeded")
	}
	bus.Close()
}
