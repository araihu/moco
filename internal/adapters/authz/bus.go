package authz

import (
	"context"
	"errors"
	"sync"

	"github.com/araihu/moco/internal/core/ports"
)

// ErrPolicyChangesBusClosed reports an operation after bus shutdown.
var ErrPolicyChangesBusClosed = errors.New("policy changes bus is closed")

// MemoryPolicyChangesBus broadcasts coalesced invalidation signals to local
// policy reloaders. Signals carry no state; subscribers reload from SQLite.
type MemoryPolicyChangesBus struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[uint64]chan struct{}
	closed      bool
}

var _ ports.PolicyChangesBus = (*MemoryPolicyChangesBus)(nil)

// NewMemoryPolicyChangesBus creates an empty in-process policy bus.
func NewMemoryPolicyChangesBus() *MemoryPolicyChangesBus {
	return &MemoryPolicyChangesBus{subscribers: make(map[uint64]chan struct{})}
}

// Publish broadcasts one coalesced signal to every active subscriber.
func (b *MemoryPolicyChangesBus) Publish(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrPolicyChangesBusClosed
	}
	for _, subscriber := range b.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	return nil
}

// Subscribe registers a buffered signal channel until ctx is canceled.
func (b *MemoryPolicyChangesBus) Subscribe(ctx context.Context) (<-chan struct{}, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrPolicyChangesBusClosed
	}
	b.nextID++
	id := b.nextID
	channel := make(chan struct{}, 1)
	b.subscribers[id] = channel
	b.mu.Unlock()
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			b.remove(id)
		}()
	}
	return channel, nil
}

// Close terminates all current subscriptions. It is intended for process
// shutdown and is idempotent.
func (b *MemoryPolicyChangesBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, subscriber := range b.subscribers {
		delete(b.subscribers, id)
		close(subscriber)
	}
}

func (b *MemoryPolicyChangesBus) remove(id uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if subscriber, ok := b.subscribers[id]; ok {
		delete(b.subscribers, id)
		close(subscriber)
	}
}
