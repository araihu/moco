# Multi-principal Access Control Slice

## Scope

The public contract defines bearer authentication and 401/403 responses. The
runtime uses an explicit keyring of principals and evaluates requests with a
static Casbin policy set. Policy snapshots are persisted and revision-guarded;
the restricted internal endpoint is documented separately from the public API.

## Credential handling

The deployment may keep the legacy `MOCO_BEARER_TOKEN`, which maps to the same
stable SHA-256-derived principal ID used by earlier releases and grants the
legacy full-access policy. For multiple principals, `MOCO_AUTH_CONFIG` points to
a strict JSON file containing `id` and lowercase `tokenSha256` fields. Clear
tokens are never parsed from this file or retained by the authenticator. The two
configuration modes are mutually exclusive. Authentication compares every
configured digest before returning a principal, and malformed or duplicate
credentials fail startup.

## Authorization model

Casbin evaluates `principal`, `domain`, `resource`, and HTTP method. Policies are
allow-only and default-deny:

```text
(principal == policy.subject || principal inherits policy.subject in domain)
  && (policy.domain == "*" || request.domain == policy.domain)
  && keyMatch3(request.resource, policy.path)
  && (policy.method == "*" || request.method == policy.method)
```

The request domain is `*` for service discovery and tenant collection routes; it
is the tenant ID for every tenant-scoped route. A path placeholder is a shape
matcher, not an authorization boundary. Policies that isolate one tenant must
therefore set that literal tenant ID in `domain`; global access must opt in with
`domain: "*"`. Tenant collection discovery accepts a tenant-scoped policy as
well: the server checks at least one configured domain for the collection route
and filters each returned tenant through its canonical item permission. A
principal with a global collection policy receives the complete collection.
Secret logical paths remain intentionally out of the policy resource because
they arrive in a query parameter; authorization is at vault granularity until a
path-aware policy design is specified.

The public `GET /api/v1/watch` checkpoint is process-wide and therefore requires
an explicit `domain: "*"` policy. Tenant-scoped permissions cannot authorize the
operation, preventing a tenant-limited principal from observing revisions caused
by another tenant.

Authorization runs after bearer authentication and before request-body parsing.
Unknown paths and unsupported methods are left to the generated router so they
retain 404/405 behavior. `HEAD` is evaluated as `GET`, matching Go's `ServeMux`
semantics for GET routes. Internal `/livez` and `/readyz` probes remain outside
the public authorizer.

## Deferred work

Token issuance, refresh, revocation, and introspection remain deployment-owned
operations and are intentionally absent from the public API. A future policy
slice may add path-aware secret authorization and a richer event stream; the
current public watch endpoint reports only a durable checkpoint and requires a
full resync after it advances.
