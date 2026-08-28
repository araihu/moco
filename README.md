# Mocó

Mocó is an API-first, self-hosted secret-management service. Its product
direction combines tenant isolation, S3-like logical secret paths, and envelope
encryption.

## Current status

The public and internal OpenAPI contracts are source-controlled and validated.
The current executable vertical slices implement tenant and vault lifecycles:

- bearer-authenticated tenant and tenant-scoped vault create, list, get, replace, and delete;
- SQLite persistence with embedded startup migrations and sqlc-generated queries;
- strong ETags for conditional reads and compare-and-swap mutations;
- creation idempotency scoped to the authenticated principal for at least 24 hours;
- HMAC-authenticated, expiring cursors over stable insertion snapshots;
- unauthenticated `/livez` and `/readyz` process probes.

Secret routes remain in the contract but return a typed
`503 capability_unavailable`; they are not advertised by `GET /api/v1`. Encryption,
Casbin authorization, multi-principal authentication, and production deployment
are not implemented yet. Do not use this slice to store secrets.

## Run locally

Go 1.27.0 is required. Supply two independent, high-entropy deployment secrets:

```bash
export MOCO_BEARER_TOKEN="$(openssl rand -hex 32)"
export MOCO_CURSOR_HMAC_KEY="$(openssl rand -hex 32)"
make run
```

The server listens on `:8080` and stores data in `./moco.db` by default. Override
those values with `MOCO_ADDR` and `MOCO_DB_PATH`. Startup automatically applies
embedded migrations. The listener is plain HTTP; terminate TLS in front of it
before sending bearer credentials across a network.

Example tenant creation:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer $MOCO_BEARER_TOKEN" \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: example-tenant-001' \
  --data '{"name":"production","labels":{}}' \
  http://127.0.0.1:8080/api/v1/tenants
```

## Architecture

Mocó uses ports and adapters. `internal/core` contains domain validation, port
contracts, and transport-independent tenant services. It imports neither HTTP
nor SQL. Infrastructure under `internal/adapters` implements those ports, and
`cmd/moco-server` is the composition root.

- `openapi/`: exploded public/internal contracts, lint rules, and bundles.
- `tools/`: isolated Go module with pinned development binaries.
- `db/migrations/`: SQLite migrations embedded into the server.
- `db/queries/`: sqlc query sources.
- `internal/core/`: domain types, ports, and application services.
- `internal/adapters/db/`: SQLite/sqlc repository.
- `internal/adapters/http/`: generated strict contract and handwritten adapter.
- `internal/adapters/authz/`: reserved for the future Casbin adapter.

The planned Casbin adapter will load persisted rules through sqlc, commit policy
changes before publishing to `PolicyChangesBus`, and trigger authoritative policy
reloads on every instance.

## Development

Tool versions are pinned under `tools/`, so global installations are unnecessary.
`actionlint` has its own nested Go module to keep its YAML dependency isolated
from Vacuum:

- actionlint `v1.7.12`
- Vacuum `v0.30.1`
- oapi-codegen `v2.8.0`
- sqlc `v1.31.1`
- govulncheck `v1.7.0`
- a SQLite-only migration runner backed by golang-migrate `v4.19.1`

golangci-lint `v2.13.2` is the exception: `make lint` downloads its official
precompiled release into the ignored `tools/bin/` directory. The upstream
installer is pinned by commit, and it verifies the release archive checksum.

```bash
make workflow-lint
make spec-lint
make api-generate
make sqlc-generate
make db-migrate DB_PATH=./moco.db
make test
make lint
make vulncheck
make check
```

`make check` runs generation, tests, `golangci-lint`, `govulncheck` source
analysis, and vulnerability scans of every pinned tool binary. CI invokes the
same gate through Dagger, explicitly builds every Go-managed tool first, and
rejects stale generated files. The Dagger module reuses immutable shared modules
from `araihu/dagger`; `golangci-lint` remains a checksum-verified precompiled
binary.

Public API source lives in `openapi/public.yaml`; probe-only internal operations
live in `openapi/internal.yaml`. Both are exploded into paths and reusable
components. Generated bundles under `openapi/bundled/` and generated Go code are
checked in for deterministic review.

## Secret addressing

The deferred secret API uses S3-like logical paths in query parameters. Item
operations use `/secret?path=prod/db/password`; metadata listing uses
`/secrets?prefix=prod/db/`. Paths forbid empty, `.`, and `..` segments, and
metadata operations never return values.
