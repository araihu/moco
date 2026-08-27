# Mocó Bootstrap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a validated, uncommitted scaffold for the headless `github.com/araihu/moco` Go service.

**Architecture:** Keep domain and port packages dependency-free except for the Go standard library. Generate a strict `net/http` adapter from one OpenAPI document, and configure future SQLite/sqlc and Casbin integration without inventing persistence or business logic.

**Tech Stack:** Go 1.27.0, OpenAPI 3.0.3, oapi-codegen v2.8.0, Vacuum v0.30.0, sqlc v1.31.1, go-migrate v4.19.1, future Casbin v2.

**Spec:** `docs/superpowers/specs/2026-08-24-moco-bootstrap-design.md`

## Global Constraints

- Module path is exactly `github.com/araihu/moco`; Go language version is exactly `1.27.0`.
- No UI, SDK/client generation, business logic, database schema, migration, authentication implementation, encryption implementation, deployment, remote, or commit.
- `internal/core` imports no HTTP, database, Casbin, framework, or generated adapter package.
- Secret item addressing uses `path` and listing uses `prefix` on the approved `/secrets` collection route.
- Generated HTTP code includes models, `std-http-server`, and `strict-server`; it excludes client and embedded-spec generation.
- Tool commands pin exact versions and run with `GOWORK=off`.
- Preserve all work as intentional uncommitted local files for user review.

## File Map

- `go.mod`, `go.sum`: application module and generated HTTP runtime dependency.
- `.gitignore`: local SQLite and macOS artifacts only.
- `README.md`: vision, boundaries, architecture, and exact developer commands.
- `Makefile`: pinned lint, generation, and migration commands.
- `api/openapi.yaml`: sole HTTP contract source.
- `api/vacuum.yaml`: recommended OpenAPI lint ruleset.
- `api/oapi-codegen.yaml`: strict standard-library server generation.
- `cmd/moco-server/doc.go`: future composition-root package.
- `db/migrations/.gitkeep`, `db/queries/.gitkeep`: reserved go-migrate/sqlc inputs.
- `sqlc.yaml`: SQLite generation configuration.
- `internal/core/domain/doc.go`: domain boundary documentation.
- `internal/core/ports/doc.go`, `internal/core/ports/policy_changes.go`: port boundary and policy-change bus.
- `internal/core/services/doc.go`: future application-service boundary.
- `internal/adapters/db/doc.go`: future sqlc repository adapter boundary.
- `internal/adapters/http/doc.go`, `internal/adapters/http/moco.gen.go`: adapter documentation and generated strict server contract.
- `internal/adapters/authz/doc.go`: future custom Casbin adapter boundary.

---

### Task 1: Initialize Module and Hexagonal Boundaries

**Files:**
- Create: `.gitignore`
- Create: `go.mod`
- Create: `cmd/moco-server/doc.go`
- Create: `db/migrations/.gitkeep`
- Create: `db/queries/.gitkeep`
- Create: `internal/core/domain/doc.go`
- Create: `internal/core/ports/doc.go`
- Create: `internal/core/ports/policy_changes.go`
- Create: `internal/core/services/doc.go`
- Create: `internal/adapters/db/doc.go`
- Create: `internal/adapters/http/doc.go`
- Create: `internal/adapters/authz/doc.go`

**Interfaces:**
- Consumes: approved design only.
- Produces: `ports.PolicyChangesBus` with `Publish(context.Context) error` and `Subscribe(context.Context) (<-chan struct{}, error)`.

- [ ] **Step 1: Initialize local repository and Go module**

Run:

```bash
git init --initial-branch=main
GOWORK=off go mod init github.com/araihu/moco
```

Expected: local `.git/` exists, `go.mod` contains module path, no remote exists.

- [ ] **Step 2: Pin exact Go language version**

Edit `go.mod` to:

```go
module github.com/araihu/moco

go 1.27.0
```

- [ ] **Step 3: Add minimal ignore rules**

Create `.gitignore`:

```gitignore
.DS_Store
moco.db
moco.db-shm
moco.db-wal
```

- [ ] **Step 4: Create package scaffolds**

Each `doc.go` contains only a package comment and declaration. Use package names `main`, `domain`, `ports`, `services`, `db`, `httpapi`, and `authz` matching its directory. Example:

```go
// Package domain defines Mocó's tenant-isolated secret-management concepts.
package domain
```

Create empty `.gitkeep` files under both DB input directories. Do not add entities, repository interfaces, a server `main`, SQL, or Casbin imports.

- [ ] **Step 5: Add minimal policy-change bus port**

Create `internal/core/ports/policy_changes.go`:

```go
package ports

import "context"

// PolicyChangesBus distributes signals that persisted authorization policy changed.
type PolicyChangesBus interface {
	Publish(context.Context) error
	Subscribe(context.Context) (<-chan struct{}, error)
}
```

The empty signal is deliberate: consumers reload authoritative policy from the database, so payload and revision types are deferred until persistence semantics exist.

- [ ] **Step 6: Verify core dependency boundary and compilation**

Run:

```bash
find cmd internal -name '*.go' -type f -print0 | xargs -0 gofmt -w
GOWORK=off go list -deps ./internal/core/... | rg 'net/http|database/sql|casbin|internal/adapters' && exit 1 || true
GOWORK=off go test ./...
```

Expected: dependency scan prints nothing; package compilation passes with no tests.

Do not commit.

### Task 2: Define and Lint OpenAPI Contract

**Files:**
- Create: `api/openapi.yaml`
- Create: `api/vacuum.yaml`
- Create: `Makefile`

**Interfaces:**
- Consumes: approved secret route and tenant/vault hierarchy.
- Produces: operation IDs `listTenants`, `createTenant`, `getTenant`, `updateTenant`, `deleteTenant`, `listVaults`, `createVault`, `getVault`, `updateVault`, `deleteVault`, `getSecrets`, `putSecret`, and `deleteSecret`.

- [ ] **Step 1: Create the ruleset and failing lint target**

Create `api/vacuum.yaml`:

```yaml
extends: [[vacuum:oas, recommended]]
```

Create the first `Makefile` slice:

```make
GO := go
VACUUM_VERSION := v0.30.0

.PHONY: spec-lint
spec-lint:
	GOWORK=off $(GO) run github.com/daveshanley/vacuum@$(VACUUM_VERSION) lint --ruleset api/vacuum.yaml -d api/openapi.yaml
```

Run `make spec-lint`.

Expected: FAIL because `api/openapi.yaml` does not exist.

- [ ] **Step 2: Write the OpenAPI document**

Create OpenAPI 3.0.3 metadata:

```yaml
openapi: 3.0.3
info:
  title: Mocó API
  version: 0.1.0
  description: Tenant-isolated, path-based secret management API.
  contact:
    name: AraiHu
    url: https://github.com/araihu/moco
tags:
  - name: Tenants
    description: Tenant lifecycle operations.
  - name: Vaults
    description: Tenant-scoped vault lifecycle operations.
  - name: Secrets
    description: Vault-scoped secret value and prefix operations.
security:
  - bearerAuth: []
```

Define these exact path/method pairs:

```text
GET,POST           /api/v1/tenants
GET,PATCH,DELETE   /api/v1/tenants/{tenantId}
GET,POST           /api/v1/tenants/{tenantId}/vaults
GET,PATCH,DELETE   /api/v1/tenants/{tenantId}/vaults/{vaultId}
GET,PUT,DELETE     /api/v1/tenants/{tenantId}/vaults/{vaultId}/secrets
```

For `GET /secrets`, define optional `path` and `prefix` query parameters and state in the operation description that exactly one is required. Return `oneOf: [Secret, SecretList]` for HTTP 200. Require `path` for PUT and DELETE. Every path operation has a unique operation ID, summary, description, tag, success response, and reusable 400/401/403/404/409/500 Problem responses as applicable.

Define reusable components:

```text
parameters: TenantId, VaultId, SecretPath, SecretPrefix
securitySchemes: bearerAuth (http/bearer)
schemas: Tenant, TenantCreate, TenantUpdate, Vault, VaultCreate, VaultUpdate,
         SecretMetadata, Secret, SecretList, SecretWrite, Problem
responses: BadRequest, Unauthorized, Forbidden, NotFound, Conflict, InternalError
```

Use UUID strings for tenant/vault IDs, RFC 3339 `date-time` timestamps, non-empty names, base64 `format: byte` secret values, and `application/problem+json` errors. Secret paths have 1–1024 characters and descriptions forbid empty, `.`, and `..` segments. Lists expose `SecretMetadata`, never values.

- [ ] **Step 3: Run lint and fix contract-only findings**

Run:

```bash
make spec-lint
```

Expected: exit 0 with valid references and no error-severity findings. Fix only the document or ruleset syntax; do not weaken the recommended ruleset.

Do not commit.

### Task 3: Generate Strict Standard-Library HTTP Contract

**Files:**
- Create: `api/oapi-codegen.yaml`
- Create: `internal/adapters/http/moco.gen.go`
- Modify: `Makefile`
- Modify: `go.mod`
- Create: `go.sum`

**Interfaces:**
- Consumes: operation IDs and schemas from `api/openapi.yaml`.
- Produces: generated `httpapi.StrictServerInterface`, request/response objects, models, and `net/http` handler registration.

- [ ] **Step 1: Add generator configuration**

Create `api/oapi-codegen.yaml`:

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/oapi-codegen/oapi-codegen/v2.8.0/configuration-schema.json
package: httpapi
generate:
  models: true
  std-http-server: true
  strict-server: true
  client: false
  embedded-spec: false
output: internal/adapters/http/moco.gen.go
```

- [ ] **Step 2: Add pinned generation target**

Append to `Makefile`:

```make
OAPI_CODEGEN_VERSION := v2.8.0

.PHONY: api-generate
api-generate:
	GOWORK=off $(GO) run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION) --config api/oapi-codegen.yaml api/openapi.yaml
```

- [ ] **Step 3: Generate code and resolve runtime dependency**

Run:

```bash
make api-generate
GOWORK=off go get github.com/oapi-codegen/runtime@v1.7.0
GOWORK=off go mod tidy
```

Expected: generated package compiles and `go.mod` contains application runtime dependencies, not generator CLI dependencies.

- [ ] **Step 4: Prove server-only generation**

Run:

```bash
rg -n 'type StrictServerInterface|func HandlerFromMux' internal/adapters/http/moco.gen.go
if rg -n 'type Client|NewClient' internal/adapters/http/moco.gen.go; then exit 1; fi
GOWORK=off go test ./...
```

Expected: strict interface and stdlib handler exist; client symbols do not; compilation passes.

- [ ] **Step 5: Prove deterministic regeneration**

Run:

```bash
shasum -a 256 internal/adapters/http/moco.gen.go > /tmp/moco-api.before
make api-generate
shasum -a 256 -c /tmp/moco-api.before
```

Expected: checksum reports `OK`.

Do not commit.

### Task 4: Configure SQLite Tooling and Developer Documentation

**Files:**
- Create: `sqlc.yaml`
- Modify: `Makefile`
- Create: `README.md`

**Interfaces:**
- Consumes: reserved `db/migrations` and `db/queries` directories.
- Produces: future sqlc output package `internal/adapters/db/sqlc`; `make sqlc-generate` and `make db-migrate DB_PATH=<path>` commands.

- [ ] **Step 1: Configure sqlc without inventing schema**

Create `sqlc.yaml`:

```yaml
version: "2"
sql:
  - engine: sqlite
    schema: db/migrations
    queries: db/queries
    gen:
      go:
        package: sqlc
        out: internal/adapters/db/sqlc
        sql_package: database/sql
        emit_json_tags: true
        emit_empty_slices: true
```

- [ ] **Step 2: Add pinned sqlc and migration targets**

Append to `Makefile`:

```make
SQLC_VERSION := v1.31.1
MIGRATE_VERSION := v4.19.1
DB_PATH ?= ./moco.db

.PHONY: sqlc-generate db-migrate
sqlc-generate:
	GOWORK=off $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

db-migrate:
	GOWORK=off $(GO) run -tags sqlite github.com/golang-migrate/migrate/v4/cmd/migrate@$(MIGRATE_VERSION) -path db/migrations -database "sqlite3://$(DB_PATH)" up
```

Do not run either target: the approved bootstrap intentionally contains no schema, query, or migration. Verify configuration shape with:

```bash
GOWORK=off go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 version
```

Expected: `v1.31.1`.

- [ ] **Step 3: Write README**

Document:

1. Mocó vision: tenant isolation, S3-like logical paths, envelope encryption, API-first self-hosting.
2. Explicit security status: encryption, authentication, persistence, and Casbin enforcement are not implemented in this scaffold.
3. Ports/adapters directory map and dependency direction.
4. Custom Casbin adapter plan: sqlc-backed `LoadPolicy`, commit-before-publish, and bus-triggered `LoadPolicy` on every instance.
5. Tool versions and prerequisites: Go 1.27.0 only; tools run through pinned `go run` commands.
6. Commands:

```bash
make spec-lint
make api-generate
make sqlc-generate                 # after schema and queries exist
make db-migrate DB_PATH=./moco.db  # after migrations exist
GOWORK=off go test ./...
```

7. Approved secret examples using `?path=prod/db/password` and `?prefix=prod/db/`.
8. Current non-goals: UI, client SDK, other databases, production deployment.

Do not claim the server is runnable or security-complete.

- [ ] **Step 4: Check documentation and Makefile formatting**

Run:

```bash
make -n spec-lint api-generate sqlc-generate db-migrate
rg -n 'tenant|prefix|envelope|Casbin|PolicyChangesBus|make spec-lint|make api-generate|make sqlc-generate|make db-migrate' README.md
```

Expected: dry-run prints four pinned commands; every required README subject is present.

Do not commit.

### Task 5: Final Bootstrap Verification

**Files:**
- Modify only files rejected by gates above.

**Interfaces:**
- Consumes: all prior outputs.
- Produces: reviewable uncommitted scaffold with fresh verification evidence.

- [ ] **Step 1: Format and inspect generated state**

Run:

```bash
find cmd internal -name '*.go' -type f -print0 | xargs -0 gofmt -w
git status --short --untracked-files=all
git remote -v
```

Expected: only intentional scaffold/spec/plan files; no remote output.

- [ ] **Step 2: Run complete applicable gates**

Run:

```bash
make spec-lint
make api-generate
GOWORK=off go test ./... -count=1
GOWORK=off go vet ./...
GOWORK=off go mod verify
if rg -n '[[:blank:]]+$|^(<<<<<<<|=======|>>>>>>>)' --glob '!.git/**' .; then exit 1; fi
```

Expected: lint, generation, tests, vet, and module verification pass. Whitespace/conflict-marker scan reports no matches across tracked and untracked project files.

- [ ] **Step 3: Recheck architectural exclusions**

Run:

```bash
if GOWORK=off go list -deps ./internal/core/... | rg 'net/http|database/sql|casbin|internal/adapters'; then exit 1; fi
if rg -n 'type Client|NewClient' internal/adapters/http/moco.gen.go; then exit 1; fi
find db/migrations db/queries -type f ! -name .gitkeep -print
git remote -v
```

Expected: no forbidden core dependencies, client symbols, SQL files, or remotes.

- [ ] **Step 4: Report exact handoff**

Report created files, pinned tool versions, gate results, and `git status --short --untracked-files=all`. State explicitly that sqlc generation and migrations were not run because their inputs are intentionally deferred. State no commit, remote, UI, or social metadata exists.

Stop. Do not commit, push, create GitHub repository, release, or begin business logic.
