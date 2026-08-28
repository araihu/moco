# Authorization Policy Persistence Slice

## Scope

This slice adds the persistence and reload primitives for the static Casbin
policy model. Role bindings and allow policies are stored as one replaceable
SQLite snapshot, and an application service publishes a change signal only
after the repository transaction commits. Administrative HTTP operations remain
a later slice; startup selection and bootstrap are covered by the follow-up
startup slice.

## Persistence contract

`ports.AuthorizationRepository` exposes `LoadAuthorization` and
`ReplaceAuthorization`. Replacement deletes and inserts the complete snapshot
inside one transaction; a failed insert rolls back the previous snapshot. SQL
queries return stable ordering so identical state produces deterministic
Casbin reloads. The schema validates bounded text lengths and uniqueness while
the Casbin adapter remains the authoritative validator for path and method
syntax.

## Publication and reload

`services.AuthorizationPolicyService` calls the repository first and publishes
through `ports.PolicyChangesBus` only after the repository returns success.
`authz.MemoryPolicyChangesBus` broadcasts a buffered, coalesced invalidation to
local subscribers; payloads are intentionally empty because subscribers reload
the authoritative snapshot from SQLite. `authz.PolicyReloader` performs one
initial load and then atomically swaps a validated Casbin enforcer for each
signal. If a candidate is invalid, the old policy remains active and the
reloader reports the error.

## Deferred work

This slice does not add policy/principal administration endpoints, distributed
bus transport, audit records, or token issuance/revocation. Startup selection
and bootstrap are specified in
`2026-08-28-policy-startup-slice.md`. Those pieces must preserve the
tenant-domain invariant and commit-before-publish ordering.
