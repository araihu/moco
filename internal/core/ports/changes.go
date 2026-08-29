package ports

import "context"

// ResourceVersionReader exposes the durable, process-wide resource revision.
// The value advances whenever a persisted resource row changes and is safe to
// use as a watch checkpoint across server processes.
type ResourceVersionReader interface {
	CurrentResourceVersion(context.Context) (int64, error)
}
