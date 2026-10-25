# Mocó

Mocó is an API-first, self-hosted secret-management service. Its product
direction combines tenant isolation, S3-like logical secret paths, and envelope
encryption.

## Current status

The public and internal OpenAPI contracts are source-controlled and validated.
The current executable vertical slices implement tenant, vault, and encrypted
secret lifecycles:

- bearer-authenticated tenant and tenant-scoped vault create, list, get, replace, and delete;
- path-based secret create/replace, value read, metadata read/list, and conditional delete;
- HKDF-SHA-256/AES-256-GCM envelope encryption with one random wrapped data key per vault;
- deployment root-key keyrings with read support for previous eras and bounded online vault-key rewrapping;
- multi-principal bearer authentication from token digests and default-deny Casbin policies;
- SQLite authorization policy snapshots with atomic revision-guarded replacement, coalesced local signals, and shared-store polling reloads;
- restricted internal `GET`/`PUT /internal/v1/authorization` snapshot administration;
- restricted internal `GET /internal/v1/audit` request metadata ledger;
- restricted internal `POST /internal/v1/audit/retention` bounded local ledger retention;
- local read-only `moco-audit-export` JSONL audit snapshots;
- complete live SQLite recovery archives with checksums, required key-era IDs, and verified offline restoration;
- a single-instance SQLite production profile with an OCI image, native TLS, private administration/metrics, verified automatic mirrored backups, retention and operational recovery drills;
- restricted internal `POST /internal/v1/encryption/rotation` bounded root-key rewrap batches;
- SQLite persistence with embedded startup migrations and sqlc-generated queries;
- strong ETags for conditional reads and compare-and-swap mutations;
- creation idempotency scoped to the authenticated principal for at least 24 hours;
- HMAC-authenticated, expiring cursors over stable insertion snapshots;
- durable `resourceVersion` watch polling for cross-process reconciliation;
- tenant-scoped `trv-*` watch polling for least-privilege reconciliation;
- unauthenticated `/livez` and `/readyz` process probes.
- optional native HTTPS with locally supplied certificate and private key files.

Secret metadata operations never select or return ciphertext or plaintext, and
secret-bearing reads use `Cache-Control: no-store`. External KMS/HSM providers,
and cross-host policy transport when instances do not share the SQLite store
are not implemented. Audit shipping and independent deployment custody remain
operator responsibilities.
Use the [single-instance production runbook](docs/operations/production-sqlite.md)
to deploy and validate this profile. Complete target-environment recovery,
notification, capacity and security checks before storing production secrets.

## Run locally

Go 1.27.3 or newer is required; mise selects the latest stable Go. Supply
independent high-entropy credentials plus one base64-encoded 256-bit encryption key:

```bash
export MOCO_BEARER_TOKEN="$(openssl rand -hex 32)"
export MOCO_CURSOR_HMAC_KEY="$(openssl rand -hex 32)"
export MOCO_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export MOCO_ENCRYPTION_KEY_ID="local-v1"
make run
```

The server listens on `:8080` and stores data in `./moco.db` by default. Override
those values with `MOCO_ADDR` and `MOCO_DB_PATH`. Startup automatically applies
embedded migrations. To enable native HTTPS, set both `MOCO_TLS_CERT_FILE` and
`MOCO_TLS_KEY_FILE` to PEM files and optionally set `MOCO_ADDR=:8443`. TLS 1.2 or
newer is required. Certificates are loaded and validated as a matching pair
before opening the database; missing or invalid files abort startup. Restart
the server after replacing certificates. Probes use HTTPS on the same listener.
Certificate issuance and renewal are managed by the deployment. When both
variables are absent the listener uses HTTP, suitable for local development or
a deployment with TLS termination in front of it.

`MOCO_ENCRYPTION_KEY_ID` is stored with wrapped vault keys and defaults to
`local-v1`. Keep the exact encryption key with database backups. For a rotation,
set `MOCO_ENCRYPTION_KEYS` to one strict JSON document containing the active key
ID and up to 16 standard-base64 256-bit keys, retaining the previous key until
all vaults are rewrapped:

```bash
export MOCO_ENCRYPTION_KEYS="$(jq -cn \
  --arg old "$(printf %s "$OLD_KEY_B64")" \
  --arg active "$(printf %s "$ACTIVE_KEY_B64")" \
  '{activeKeyId:"root-v2",keys:{"root-v1":$old,"root-v2":$active}}')"
export MOCO_ENCRYPTION_KEY_EPOCH="2"
export MOCO_ENCRYPTION_KEY=""
```

The active key ID is used for new vaults. Previous IDs remain read-capable, and
`POST /internal/v1/encryption/rotation` rewraps at most 200 vault keys per call;
continue with both returned checkpoints, then run fresh sweeps until
`complete` is true and `remainingOldKeys` is zero. The epoch is a monotonically
increasing shared-store fence: bump it with each active-era rollout and start
all writers with the new key ID and epoch before rotating. Processes from an
older epoch can still serve compatible reads but cannot write secrets or run
rotation. Remove the retired key only after a backup and verification. Repeating
a page is safe. The operation changes only wrapped-key bytes, does not expose
key material, and does not advance the public resource-watch revision. Do not
configure a keyring and the legacy `MOCO_ENCRYPTION_KEY` at the same time.

`GET /internal/v1/encryption/status` is a read-only deployment diagnostic. It
reports the shared active root-key ID, epoch, and current count of vault keys
still using an older era. Run a fresh rotation sweep and confirm
`complete=true` with `remainingOldKeys=0` before removing an old key from the
deployment keyring; the status is point-in-time and does not reserve the state.

The local `MOCO_BEARER_TOKEN` mode creates one backwards-compatible principal
with full API access. Multi-principal deployments set `MOCO_AUTH_CONFIG` to a
JSON file containing only SHA-256 token digests, role bindings, and allow
policies, and leave `MOCO_BEARER_TOKEN` unset:

```json
{
  "principals": [{"id": "controller", "tokenSha256": "<64 lowercase hex characters>"}],
  "roleBindings": [{"principal": "controller", "role": "secret-reader", "domain": "11111111-1111-4111-8111-111111111111"}],
  "policies": [{
    "subject": "secret-reader",
    "domain": "11111111-1111-4111-8111-111111111111",
    "path": "/api/v1/tenants/{tenantId}/vaults/{vaultId}/secret",
    "method": "GET"
  }]
}
```

On the first start with `MOCO_AUTH_CONFIG`, role bindings and policies are
validated and persisted as one SQLite snapshot. Once initialized, that
snapshot is authoritative across restarts; changing the file's role bindings or
policies does not silently replace it. Principals and their token digests stay
in the file because they are needed to authenticate requests. An intentionally
empty role/policy set is also persisted and remains default-deny.

The internal authorization endpoint requires an explicit Casbin policy for
`/internal/v1/authorization` with `GET` and/or `PUT`; it is not part of the
public API. Keep that permission on a dedicated deployment principal and
restrict the direct deployment origin with network policy as well.

The internal audit endpoint requires its own explicit `GET` policy for
`/internal/v1/audit`. Successful API mutations and their audit event commit in
one SQLite transaction, before success headers or a body are sent. An audit or
commit failure rolls back the complete mutation and returns `503`, including
tenant/vault cascades, idempotency, watch revisions, new vault keys, policy
changes, retention and each rotation batch. Startup epoch/policy initialization
also commits with a `STARTUP /internal/startup` event from `system:startup`.
The ledger includes status and principal metadata, without request bodies,
query strings, bearer credentials, or plaintext secret paths. The audit read
itself is excluded so a tailing client cannot create a self-sustaining stream
of its own polls. Logical secret paths and list prefixes are represented only
by keyed HMAC-SHA-256 digests derived with a domain-separated label from
`MOCO_CURSOR_HMAC_KEY`. Rotating that key intentionally breaks correlation of
path digests across key eras. The ledger is for security observability, not a
reconciliation/change feed; controllers and operators use `/api/v1/watch` plus
relist for convergence. Read-only requests and rejected attempts keep independent
post-response audit logging; a failure there cannot undo a delivered read. Audit
failures are logged and monitored. Policy invalidations publish only after the
audited commit; a notification failure keeps the committed success response and
is logged, while SQLite polling allows reload convergence. See the
[transactional audit runbook](docs/operations/transactional-audit.md) for guarantees,
failure behavior and operational tests.

The internal audit-retention endpoint requires its own explicit `POST` policy
for `/internal/v1/audit/retention`. For deletion, supply a cutoff at least one
hour old and repeat the bounded request until `complete` is true and `remaining`
is zero. Set `dryRun=true` first to preview the current matching count (even
with a more recent past cutoff); a dry run never deletes and is not a
convergence loop. Retention deletes only ledger rows older than the cutoff,
preserves monotonic sequence allocation, and does not advance the public
resource-watch revision. The response is a current diagnostic rather than a
snapshot; keep the operation on the deployment origin and export any retained
data before purging it.

For offline backup, run `moco-audit-export` against the SQLite file. It opens the
database read-only, captures the current highest audit sequence as a finite
snapshot boundary, streams one metadata object per JSONL line, and writes a new
destination with mode `0600` without overwriting an existing file. Use
`--after-sequence` to resume after a previous export; rows appended after the
snapshot boundary are intentionally left for the next run. `-output -` streams
JSONL to stdout and leaves the caller responsible for protecting the pipe.
Supplying `--manifest` writes a second private JSON file containing the
snapshot bounds, count, and SHA-256 of the exact JSONL bytes; pass both files
back with `--verify` to validate the checksum, schema, ordering, and bounds
without opening SQLite.

```bash
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
go run ./cmd/moco-audit-export \
  --database ./moco.db \
  --output "./audit-$STAMP.jsonl" \
  --manifest "./audit-$STAMP.manifest.json"

go run ./cmd/moco-audit-export \
  --verify \
  --output ./audit-20260829T000000Z.jsonl \
  --manifest ./audit-20260829T000000Z.manifest.json
```

The internal encryption rotation endpoint requires its own explicit `POST`
policy for `/internal/v1/encryption/rotation`. It is a maintenance operation,
not a public resource API; restrict it to a dedicated operator principal and
deployment origin. Key material is supplied through startup configuration, not
through HTTP.

For full disaster recovery, `moco-backup` captures all database state, including
tenants, vaults, encrypted secrets, policies, audit, idempotency, and watch/key
epochs. It uses SQLite `VACUUM INTO` against a read-only source, so committed WAL
transactions are included without migrating or stopping the source. The output
is one atomically published `0600` tar archive containing a database snapshot
and checksum manifest; existing outputs are never overwritten.

```bash
go run ./cmd/moco-backup create --database ./moco.db --output ./moco-backup.tar
go run ./cmd/moco-backup verify --backup ./moco-backup.tar
go run ./cmd/moco-backup verify --backup ./moco-backup.tar --keyring /secure/recovery-keys.json
go run ./cmd/moco-backup restore --backup ./moco-backup.tar \
  --database ./recovered.db --keyring /secure/recovery-keys.json
```

Restoration requires a new offline destination and authenticates every vault
key and secret before publishing the database. The private keyring file uses
the same JSON format as `MOCO_ENCRYPTION_KEYS`. Root keys, bearer credentials,
cursor HMAC keys, auth configuration files, and TLS private keys are deliberately
kept in separate deployment custody. Retain historical root keys for as long as
any retained backup needs them, even after live rotation is complete. Archives
also contain readable metadata and must be protected as sensitive files. See
[the recovery runbook](docs/operations/backup-restore.md) for rotation, deployment
configuration, version compatibility, and client resynchronization.

Policies are default-deny, use Casbin `keyMatch3` path patterns, and bind
tenant-scoped requests through `domain`; use the literal tenant ID for an
isolated tenant. A secret-operation policy can additionally set
`secretPathPrefix` (for example `prod/database/`) to restrict the decoded
logical secret path or list prefix to that literal prefix. The reserved value
`*` preserves legacy vault-wide behavior. Path-scoped list requests must
include a non-empty prefix. A global policy must explicitly use `domain: "*"`.
`HEAD` requests consume the corresponding `GET` permission.
Generate a digest without putting the clear token in the file:
`printf %s "$TOKEN" | sha256sum`.

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
restart/recovery/key-rotation drills. CI also exercises the OCI profile and
monitoring rules in a separate job. CI invokes the
same gate through Dagger, explicitly builds every Go-managed tool first, and
rejects stale generated files. The Dagger module reuses immutable shared modules
from `araihu/dagger`; the CI tool build includes `golangci-lint` from its isolated
module and uses the same version and source checksums as local builds.
Dagger sets `GOTOOLCHAIN=auto` so each module can select its required Go version
when the shared base image provides an older compiler.

The Dagger module selects patched OpenTelemetry v1.45.0, Logs v0.21.0, and
`golang.org/x/net` v0.60.0. Its SDK telemetry bridge needs a local compatibility
adaptation for the unified Logs value API; source provenance, license, tests,
and removal conditions are documented in
[`dagger/third_party/otel-go/README.moco.md`](dagger/third_party/otel-go/README.moco.md).

Public API source lives in `openapi/public.yaml`; probe-only internal operations
live in `openapi/internal.yaml`. Both are exploded into paths and reusable
components. Generated bundles under `openapi/bundled/` and generated Go code are
checked in for deterministic review.

## Secret addressing

The secret API uses S3-like logical paths in query parameters. Item
operations use `/secret?path=prod/db/password`; metadata listing uses
`/secrets?prefix=prod/db/`. Paths forbid empty, `.`, and `..` segments, and
metadata operations never return values.
