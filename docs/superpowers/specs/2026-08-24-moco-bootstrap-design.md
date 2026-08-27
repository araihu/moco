# Mocó Bootstrap Design

## Scope

Bootstrap `github.com/araihu/moco` as a headless, API-first Go 1.27 service.
This iteration establishes repository structure, architectural boundaries,
OpenAPI contracts, code-generation configuration, and developer commands. It
does not implement business logic, persistence, authentication, encryption,
deployment, an SDK, or a UI.

The project is initialized as a local Git repository. This iteration creates no
commit and configures no remote.

## Architecture

Mocó uses ports and adapters. `internal/core` contains domain types, port
interfaces, and future application services. It must not import HTTP, database,
Casbin, or generated adapter packages. `internal/adapters` contains HTTP,
database, and authorization adapters. `cmd/moco-server` will compose adapters
around the core, but this iteration provides only its package scaffold.

The repository starts with these boundaries:

- `api/`: OpenAPI source, Vacuum rules, and oapi-codegen configuration.
- `cmd/moco-server/`: future composition root.
- `db/migrations/` and `db/queries/`: future go-migrate and sqlc inputs.
- `internal/core/domain/`: tenant, vault, secret, and role concepts.
- `internal/core/ports/`: repositories, use cases, and policy-change bus.
- `internal/core/services/`: future use-case implementations.
- `internal/adapters/db/`: sqlc-backed repository adapters.
- `internal/adapters/http/`: generated strict HTTP contract and future handler.
- `internal/adapters/authz/`: custom Casbin adapter and enforcer wiring.

Only package documentation and required port contracts are handwritten in Go.
No speculative repository or service implementation is added.

## API Contract

`api/openapi.yaml` is the source of truth. It defines bearer-authenticated,
JSON REST operations under `/api/v1` for:

- listing and creating tenants;
- reading, updating, and deleting a tenant;
- listing and creating vaults within a tenant;
- reading, updating, and deleting a vault;
- listing secret metadata by prefix;
- writing, reading, and deleting a secret by logical path.

OpenAPI has no standard greedy path parameter. Secret operations therefore use
the collection route with a query identifier:

```text
PUT    /api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets?path=prod/db/password
GET    /api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets?path=prod/db/password
DELETE /api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets?path=prod/db/password
GET    /api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets?prefix=prod/db/
```

Logical paths remain slash-delimited and human-readable without relying on
router-specific wildcard syntax or encoded slashes. Request validation rejects
empty paths and path traversal segments. `GET /secrets` requires exactly one of
`path` or `prefix`; `PUT` and `DELETE` require `path`. List responses expose
metadata, not secret values. Errors use `application/problem+json` with a
reusable Problem schema.

Envelope encryption is documented as a product invariant but deliberately not
specified as an API-side cryptographic format. Clients submit and receive
secret values; a later persistence design will define key hierarchy, ciphertext
storage, rotation, and plaintext lifetime.

## Authorization Consistency

The core exposes a policy-change bus port independent of Casbin and transport
technology. Its minimal contract supports publishing a policy-change signal and
subscribing to signals until a context is canceled.

The future authorization adapter will:

1. reconstruct Casbin policies from sqlc queries in `LoadPolicy`;
2. persist mutations through database-backed application operations;
3. publish a change only after the database transaction commits;
4. reload each instance's in-memory enforcer when it receives a change.

No concrete bus, policy schema, Casbin model, or partial adapter is added in
this bootstrap. Those choices depend on the persistence and authorization
design and would be speculative now.

## Tooling

- Vacuum validates and lints `api/openapi.yaml` using `api/vacuum.yaml`.
- oapi-codegen produces models, strict server interfaces, and a standard-library
  HTTP server adapter; no client is generated.
- sqlc is configured for SQLite, with queries in `db/queries`, migrations as
  schema input, and generated code in `internal/adapters/db/sqlc`.
- go-migrate targets SQLite migrations but has no migration to apply in this
  iteration.
- Make targets run Go tooling with `GOWORK=off` so sibling AraiHu modules cannot
  alter dependency or generation results.

Tool versions are pinned in Make variables or Go tool dependencies so generation
is reproducible. Generated HTTP output is checked by regenerating it and testing
for a clean diff after the initial project commit; this bootstrap itself creates
no commit.

## Verification

This iteration is accepted when:

1. required directories and package documentation exist;
2. `go.mod` declares Go 1.27 and the expected module path;
3. Vacuum validates and lints the OpenAPI document;
4. oapi-codegen generates strict standard-library server code without a client;
5. generated and handwritten Go code formats and compiles;
6. sqlc configuration parses, while generation remains deferred until schema
   and queries exist;
7. `git diff --check` reports no whitespace errors;
8. repository status lists only the intentional uncommitted bootstrap files.

No web page exists, so canonical, Open Graph, X Card, and social-preview image
requirements have no applicable route in this iteration.

## Deferred Work

- domain entities and validation rules;
- repository and use-case interfaces beyond the policy bus;
- database schema, migrations, and sqlc queries;
- envelope-encryption and key-management design;
- Casbin model, adapter implementation, and authorization policies;
- concrete policy bus transport;
- authentication and server composition;
- SDK, UI, deployment, and social metadata.
