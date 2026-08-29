# Tenant-Scoped Watch Slice

## Scope

This slice adds a least-privilege watch checkpoint for one tenant while
preserving the existing process-wide watch. It uses SQLite triggers and the
existing Go HTTP boundary only; no network service, KMS/HSM integration, or
runtime dependency is introduced.

## Contract

`GET /api/v1/tenants/{tenantId}/watch` returns a `trv-<n>` checkpoint for the
tenant. Without `resourceVersion`, it returns the current checkpoint
immediately. With a checkpoint, it returns immediately when the checkpoint is
stale or waits up to the bounded `timeoutSeconds` (0 through 25) for a newer
tenant, vault, or secret mutation. `changed=true` is only a relist signal; no
payload, ordering, or tombstone stream is exposed.

The route uses the literal tenant ID as its authorization domain, so a
tenant-scoped principal can watch only that tenant. The process-wide
`/api/v1/watch` remains available for cluster-wide controllers and still
requires a global watch policy.

## Persistence and deletion

SQLite stores one monotonic checkpoint row per tenant. Existing tenants start at
zero when the migration is applied. Tenant, vault, and secret inserts, updates,
and deletes advance only the corresponding tenant row; root-key, authorization,
audit, retention, and export maintenance do not. A deleted tenant retains its
row as a tombstone so a watcher that already knows the tenant can observe the
final change. An ID that has never existed returns `404`.

The checkpoint is durable across process restarts and shared by processes that
use the same SQLite store. It is a point-in-time diagnostic, not an event log or
a consistency lock; callers must relist authorized collections after a change
and tolerate concurrent mutations.
