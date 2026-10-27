# Architecture and development

Mocó uses ports and adapters. `internal/core` contains domain validation, port
contracts, and transport-independent application services. It imports neither HTTP
nor SQL. Infrastructure under `internal/adapters` implements those ports, and
`cmd/moco-server` is the composition root.

- `openapi/`: exploded public/internal contracts, lint rules, and bundles.
- `cmd/moco-audit-export/`: read-only offline JSONL audit exporter.
- `cmd/moco-backup/`: full database backup, verification, and offline restore.
- `tools/`: isolated Go module with pinned development binaries.
- `db/migrations/`: SQLite migrations embedded into the server.
- `db/queries/`: sqlc query sources.
- `internal/core/`: domain types, ports, and application services.
- `internal/adapters/db/`: SQLite/sqlc repository.
- `internal/adapters/backup/`: streaming recovery archives and atomic publication.
- `internal/adapters/encryption/`: HKDF-SHA-256/AES-256-GCM envelope encryption adapter.
- `internal/adapters/http/`: generated strict contract and handwritten adapter, including audit capture.
- `internal/adapters/authn/`: bearer token keyring keyed by SHA-256 digests.
- `internal/adapters/authz/`: reloadable Casbin model, policy bus, and default-deny adapter.

The internal policy administration endpoint is implemented with revision-guarded
writes. Each configured server instance supervises its SQLite-backed policy
reloader, using local signals and shared-store polling, and terminates if an
authoritative snapshot cannot be loaded or validated.

The public watch endpoint is a full-resync signal: it reports only a monotonic
checkpoint and intentionally does not expose event payloads or tombstones. A
controller or operator should persist its last checkpoint, poll with a bounded
timeout, and relist the collections it is authorized to manage after a change.
The process-wide `/api/v1/watch` endpoint requires its explicit cluster-wide
watch permission. A tenant-scoped controller can instead use
`/api/v1/tenants/{tenantId}/watch`; its checkpoint advances for that tenant's
tenant, vault, and secret mutations, survives deletion as a tombstone, and
accepts a literal tenant-domain policy.

## Development

Operational drills require Python 3 and OpenSSL on the host. The separate image
and monitoring gates require Docker Engine and Compose.
The Linux load/fault suite uses `/proc` and per-child `resource.prlimit`; its
bounded acceptance profile runs in `make check`. Use `make load-test
LOAD_REPORT=/tmp/moco-load-fault.json` for a retained JSON report and see the
[load/fault runbook](operations/load-fault-tests.md) for extended workloads,
latency/resource budgets and fault guarantees.

Go tool versions are pinned under `tools/`, so global installations are unnecessary.
`actionlint` and `golangci-lint` have their own nested Go modules to keep their
dependency graphs isolated from Vacuum:

- actionlint `v1.7.12`
- Vacuum `v0.30.1`
- oapi-codegen `v2.8.0`
- sqlc `v1.31.1`
- govulncheck `v1.7.0`
- golangci-lint `v2.15.0`
- Casbin `v2.135.0`
- a SQLite-only migration runner backed by golang-migrate `v4.19.1`

Every tool is compiled from its pinned Go module using the selected Go
toolchain. Module checksums authenticate the sources, and the Go build cache
keeps repeat runs fast while rebuilding when the toolchain changes. This also
updates the standard library embedded in `golangci-lint`, rather than retaining
the older runtime in an upstream precompiled release. Its first build uses one
worker with a 4 GiB memory target; override `LINT_BUILD_GOMEMLIMIT` and
`LINT_BUILD_GOGC` when necessary. Other tools retain their 2 GiB memory target.

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
analysis, vulnerability scans of every pinned tool binary, and executable
restart/recovery/key-rotation drills and concurrent load/fault acceptance. CI
also exercises the OCI profile and monitoring rules in a separate job. CI invokes the
same gate through Dagger, explicitly builds every Go-managed tool first, and
rejects stale generated files. The Dagger module reuses immutable shared modules
from `araihu/dagger`; the CI tool build includes `golangci-lint` from its isolated
module and uses the same version and source checksums as local builds.
Dagger sets `GOTOOLCHAIN=auto` so each module can select its required Go version
when the shared base image provides an older compiler.
CLI and engine/SDK are pinned together to v0.21.11; every runner installs that
CLI and CI validates `dagger functions` before building tools. The pinned engine
endpoint overrides any older runner-wide engine so SDK bootstrap can load the
current Go module before its own build functions run.
The local SDK adapter preserves the reviewed dependency manifests after binding
generation and compiles the module with a digest-pinned Go 1.27.3 image using
`-mod=readonly`. See [load/fault acceptance and SDK compatibility](operations/load-fault-tests.md).

The Dagger module selects patched OpenTelemetry v1.45.0, Logs v0.21.0, and
`golang.org/x/net` v0.60.0. Its SDK telemetry bridge needs a local compatibility
adaptation for the unified Logs value API; source provenance, license, tests,
and removal conditions are documented in
[`dagger/third_party/otel-go/README.moco.md`](../dagger/third_party/otel-go/README.moco.md).

Public API source lives in `openapi/public.yaml`; administrative and probe operations
live in `openapi/internal.yaml`. Both are exploded into paths and reusable
components. Generated bundles under `openapi/bundled/` and generated Go code are
checked in for deterministic review.

CLI metadata is maintained separately in `openapi/overlays/cli.*.yaml`.
`make cli-spec-bundle` uses Vacuum to apply these overlays and publish
`openapi/bundled/cli/public.json` and `internal.json` for
[oapi-cli-gen](https://github.com/araihu/oapi-cli-gen). These bundles explicitly
name commands such as `moco tenants create` and `moco secrets get`, while
preserving the principal API contracts. See the
[OpenAPI pipeline](../openapi/README.md#cli-metadata-overlays).
