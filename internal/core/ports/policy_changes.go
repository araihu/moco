package ports

import "context"

// PolicyChangesBus distributes signals that persisted authorization policy changed.
type PolicyChangesBus interface {
	Publish(context.Context) error
	Subscribe(context.Context) (<-chan struct{}, error)
}
