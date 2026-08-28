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

`GET` reads the authoritative SQLite snapshot, including its initialized marker.
The endpoint never reads or serializes the configured token-digest keyring.

## Lifecycle and compatibility

Configured deployments expose the operation and run the existing policy
reloader. Legacy `MOCO_BEARER_TOKEN` deployments keep their compatibility
authorization path; the administrative operation reports service unavailable
because no persisted policy writer is composed in that mode.

Distributed change propagation, optimistic concurrency for concurrent writers,
principal administration, audit records, and token issuance/revocation remain
future slices.
