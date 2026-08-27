# Mocó OpenAPI contracts

`public.yaml` is the versioned product contract for human-operated clients,
automation, Kubernetes operators, and Kubernetes controllers. Consumers must
not assume that they run in the same cluster or network as Mocó. Every public
operation therefore uses explicit bearer authentication and is safe to expose
behind a TLS ingress when deployment policy permits it.

`internal.yaml` contains only the minimal unauthenticated liveness and
readiness probes needed by a process supervisor or load balancer. These probes
reveal no configuration or dependency details. A deployment should restrict
them with network policy instead of treating same-cluster placement as an
authentication mechanism.

The contracts are intentionally split into small files below `paths/` and
`components/`. Relative `$ref` values are part of the source contract. Run
`make spec-lint` to validate and lint both roots with the pinned Vacuum tool,
`make spec-bundle` to rebuild the single-file distributions in `bundled/`, and
`make api-generate` to prove that the bundled public contract produces a typed
Go server boundary. The bundled files are generated distribution artifacts;
edit the exploded sources instead.

## Automation guarantees

- Collection responses use opaque cursor pagination. A cursor continues a
  stable snapshot; expired cursors return `410` and consumers restart listing.
- Create requests accept `Idempotency-Key`, and resources expose immutable
  `externalId` values for deterministic discovery and adoption.
- Mutable resources expose an opaque `ETag`. Clients may use `If-Match` to
  prevent lost updates.
- Secret lists and write responses contain metadata only. Secret values appear
  only in the explicit `getSecret` response and are marked `Cache-Control:
  no-store`.
- A secret `digest` allows controllers to detect drift without reading or
  logging the value.
- `429` and `503` responses include `Retry-After`; callers must apply bounded
  retries with jitter.

Authentication token issuance, server deployment, storage configuration,
encryption-key lifecycle, and authorization policy administration are outside
this first public contract. Bearer tokens may be opaque and are not required to
be JWTs.
