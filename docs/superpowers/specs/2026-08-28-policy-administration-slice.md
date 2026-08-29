# Authorization Policy Administration Slice

## Contract boundary

The internal OpenAPI contract adds `GET` and `PUT
/internal/v1/authorization`. The public `/api/v1` contract remains unchanged.
The operation returns and accepts role bindings plus allow policies only; token
digests and principal credentials are never returned by the endpoint.

Liveness and readiness remain unauthenticated. The administration operation
requires a bearer credential and an explicit Casbin permission for the exact
internal resource path. Deployments should also restrict the direct origin with
network policy.

## Replacement semantics

`PUT` accepts a complete snapshot. Empty `roleBindings` and `policies` arrays
are valid and intentionally produce default-deny. The HTTP adapter validates
principal references against the current keyring and validates the complete
Casbin candidate before calling `AuthorizationPolicyService`. The service
commits the replacement atomically and publishes the local reload signal only
after commit. A failed validation leaves the previous snapshot untouched.

`GET` reads the authoritative SQLite snapshot, including its initialized marker
and monotonic revision. The revision is also returned as an opaque strong ETag.
The endpoint never reads or serializes the configured token-digest keyring.

## Concurrency and propagation

Writers must send the ETag they observed in `If-Match`; `*` means "use the
revision observed immediately before this request". SQLite advances the
revision conditionally in the same transaction as the complete replacement, so
concurrent writers have one winner and stale writers receive `412` without
changing the snapshot. The service still publishes only after commit.

The in-process bus gives local reloaders a fast path. Each reloader also polls
the authoritative revision (one second by default), allowing separate
processes to converge when they share the SQLite store. A deployment that puts
instances on hosts without a shared SQLite volume needs an external repository
and change transport, which is intentionally outside this slice.

## Lifecycle and compatibility

Configured deployments expose the operation and run the existing policy
reloader. Legacy `MOCO_BEARER_TOKEN` deployments keep their compatibility
authorization path; the administrative operation reports service unavailable
because no persisted policy writer is composed in that mode.

Principal administration, audit records, and token issuance/revocation remain
future slices.
