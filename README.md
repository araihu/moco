# Mocó

Mocó is a future API-first, self-hosted secret-management service. Its product
vision is tenant isolation, S3-like logical secret paths, and envelope
encryption.

## Scaffold status

This repository is a bootstrap, not a runnable server. Encryption,
authentication, persistence, and Casbin enforcement are planned but not
implemented. The OpenAPI contract describes the intended API surface; it does
not make security features available yet.

## Architecture

Mocó uses ports and adapters. `internal/core` holds domain concepts, port
contracts, and future application services. It must not depend on HTTP,
database, Casbin, or generated adapter packages. `internal/adapters` implements
outward infrastructure behind those ports; `cmd/moco-server` will eventually
compose the application.

- `api/`: OpenAPI source, lint rules, and code-generation configuration.
- `cmd/moco-server/`: future composition root.
- `db/migrations/`: future go-migrate inputs.
- `db/queries/`: future sqlc inputs.
- `internal/core/domain/`: tenant, vault, secret, and role concepts.
- `internal/core/ports/`: external-system contracts, including
  `PolicyChangesBus`.
- `internal/core/services/`: future use-case implementations.
- `internal/adapters/db/`: future sqlc-backed database adapters.
- `internal/adapters/http/`: generated HTTP contract and future handler.
- `internal/adapters/authz/`: future custom Casbin adapter and enforcer wiring.

## Authorization plan

The custom Casbin adapter will load persisted rules through sqlc-backed
`LoadPolicy`. Policy changes will commit to the database before publishing to
`PolicyChangesBus`. Every instance will subscribe to that bus and call
`LoadPolicy` to reload its in-memory policy after a change.

## Prerequisites and tooling

Go 1.27.0 is required. Tools run through pinned `go run` commands; no global
tool installation is needed.

- Vacuum `v0.30.0`
- oapi-codegen `v2.8.0`
- sqlc `v1.31.1`
- golang-migrate `v4.19.1`

## Commands

```bash
make spec-lint
make api-generate
make sqlc-generate                 # after schema and queries exist
make db-migrate DB_PATH=./moco.db  # after migrations exist
GOWORK=off go test ./...
```

## Secret addressing

Secrets use S3-like logical paths in query parameters. Item paths forbid a
trailing slash. Prefixes allow one optional trailing slash; both forms forbid
empty interior, `.`, and `..` segments. Approved examples:

```text
?path=prod/db/password
?prefix=prod/db/
```

## Current non-goals

This bootstrap has no UI, client SDK, other database support, or production
deployment. As a headless project with no UI or public page, it has no social
metadata surface.
