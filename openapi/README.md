# Mocó OpenAPI contracts

`public.yaml` is the versioned product contract for human-operated clients,
automation, Kubernetes operators, and Kubernetes controllers. Consumers must
not assume that they run in the same cluster or network as Mocó. Every public
operation therefore uses explicit bearer authentication and is safe to expose
behind a TLS ingress when deployment policy permits it.

`internal.yaml` contains the minimal unauthenticated liveness/readiness probes
needed by a process supervisor or load balancer plus bearer-protected
authorization-snapshot and request-audit operations for the restricted
deployment origin. These operations never expose token digests, request bodies,
query strings, or plaintext logical secret paths. A deployment should restrict
the origin with network policy as well as explicit Casbin permissions; same-
cluster placement is not an authentication mechanism.

The contracts are intentionally split into small files below `paths/` and
`components/`. Relative `$ref` values are part of the source contract. Run
`make spec-lint` to validate and lint both roots with the pinned Vacuum tool,
`make spec-bundle` to rebuild the single-file distributions in `bundled/`, and
`make api-generate` to prove that the bundled public and internal contracts
produce typed Go server boundaries. The bundled files are generated distribution
artifacts; edit the exploded sources instead.

## Automation guarantees

- Collection responses use opaque cursor pagination. A cursor continues a
  sequence-bounded snapshot in ascending insertion order; expired cursors
  return `410` and consumers restart listing. `hasMore=true` always carries a
  continuation cursor, while the final page carries `nextCursor=null`.
- `GET /api/v1/watch` exposes a durable `resourceVersion` long-poll. A changed
  result is a resync signal, not an event payload or tombstone stream; clients
  must list the collections they are authorized to reconcile. The checkpoint is
  process-wide and therefore requires an explicit cluster-wide watch policy;
  tenant-scoped policies cannot use it to observe another tenant.
- Create requests accept `Idempotency-Key`, and resources expose immutable
  `externalId` values for deterministic discovery and adoption. Supplying an
  external ID makes uniqueness conflict recovery atomic: a `409` includes the
  existing `resourceId` when it can be disclosed, so an operator can adopt it
  instead of creating a duplicate.
- Mutable resources expose an opaque `ETag`. Clients may use `If-Match` to
  prevent lost updates.
- The internal authorization snapshot exposes its monotonic revision as both
  `revision` and an `ETag`; `PUT /internal/v1/authorization` requires
  `If-Match` and returns `412` for a stale writer.
- `GET /internal/v1/audit` exposes an ascending sequence ledger of request
  metadata. `afterSequence` is exclusive and `nextAfterSequence` continues the
  page; query strings and request payloads are omitted, while logical secret
  paths are represented only by keyed HMAC-SHA-256 digests. The audit read
  itself is excluded to keep tailing from producing self-events. Audit writes
  happen after the response and cannot change its status.
- Secret lists and write responses contain metadata only. Secret values appear
  only in the explicit `getSecret` response and are marked `Cache-Control:
  no-store`.
- `getSecretMetadata` returns a secret `digest` so controllers can detect drift
  without reading or logging the value.
- Secret authorization is vault-scoped by default. An internal authorization
  policy may set `secretPathPrefix` on a secret item, metadata, or collection
  route; the decoded logical `path` or `prefix` query value must then begin
  with that literal prefix. The reserved value `*` preserves legacy
  vault-wide behavior. A path-scoped collection request must provide a
  non-empty prefix. Use dedicated vaults when a deployment needs isolation
  that is not expressed by a path prefix.
- `429` and `503` responses include `Retry-After`; they may be emitted by Mocó
  or by the deployment edge, and callers must apply bounded retries with jitter.

Authentication token issuance, server deployment, storage configuration,
encryption-key lifecycle, and authorization policy administration are outside
this first public contract. Bearer tokens may be opaque and are not required to
be JWTs. Deployments provision bearer credentials to a CLI, operator, or
controller through their own secret-management channel; Mocó deliberately does
not expose login, refresh, revoke, or token-introspection endpoints.
