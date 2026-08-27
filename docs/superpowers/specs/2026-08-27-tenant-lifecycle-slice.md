# Tenant Lifecycle Slice

## Scope

This slice turns the validated public contract into a runnable server for tenant
lifecycle operations only. Vault and secret routes remain contract-visible but
return the declared service-unavailable response and are omitted from service
capabilities. This prevents partial secret storage from being mistaken for a
secure implementation.

## Boundaries

The core domain and service packages use only the Go standard library. A tenant
repository port owns atomic persistence semantics. The SQLite adapter implements
that port using sqlc output, while the HTTP adapter translates generated strict
request and response objects. The command package supplies deployment secrets and
composes those dependencies.

## Persistence invariants

Tenant IDs are UUIDv4 values; names and external IDs are independently unique.
The external ID is immutable. Mutable replacements increment a monotonic revision.
Insertion sequence is immutable and separate from the public ID so list cursors
can retain a stable upper bound even while new tenants are created.

An idempotency record is keyed by authenticated principal, operation, and caller
key. It stores a canonical request hash and the original 201 representation for
24 hours. Claiming the key and inserting the tenant occur in one transaction.
An identical replay returns that stored representation; a different request hash
returns 409.

## HTTP concurrency and pagination

Tenant ETags are strong validators derived from stable ID and revision. Conditional
updates and deletes compare the supplied ETag and execute a revision-qualified SQL
mutation, preventing two writers with the same ETag from both succeeding. GET uses
weak comparison rules for `If-None-Match` and returns an empty 304.

The first list request records the maximum insertion sequence. Continuation cursors
carry that snapshot, the last visited sequence, expiration, and exact filters. The
payload is authenticated with HMAC-SHA-256. Later inserts are excluded, changed
filters or tampering return 400, and expiration returns 410.

## Security posture

This slice uses one deployment-provided opaque bearer token. Only its SHA-256 digest
is used as the idempotency principal and credentials are never logged. Production
traffic requires TLS termination outside the process. Request decoding rejects
unknown JSON fields, and Problem Details never include request bodies or caller
values.

This is not a secret-management-ready release: multi-principal authentication,
authorization, vaults, encrypted secret persistence, key rotation, auditing, and
deployment hardening remain deferred.
