package ports

import "context"

// ResourceVersionReader exposes the durable, process-wide resource revision.
// The value advances whenever a persisted resource row changes and is safe to
// use as a watch checkpoint across server processes.
type ResourceVersionReader interface {
	CurrentResourceVersion(context.Context) (int64, error)
}

// TenantResourceVersionReader exposes a durable resource revision scoped to a
// tenant. The checkpoint survives tenant deletion as a tombstone so a watcher
// that already knows the tenant can observe that final change.
type TenantResourceVersionReader interface {
	CurrentTenantResourceVersion(context.Context, string) (int64, error)
}
