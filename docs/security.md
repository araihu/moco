# Security and key management

Moco encrypts secret values and wrapped vault keys. SQLite metadata and backup
metadata remain readable; protect database files, volumes and recovery archives.
The supported topology is one writer with a local SQLite filesystem.

For native HTTPS, configure both `MOCO_TLS_CERT_FILE` and `MOCO_TLS_KEY_FILE`
with matching PEM files and set `MOCO_ADDR` as needed. Startup validates the pair
before opening SQLite; TLS 1.2 or newer is required. Restart after replacing
certificates. Probes use the same scheme and listener. Certificate issuance and
renewal remain deployment responsibilities. HTTP defaults are for development;
follow the [production profile](operations/production-sqlite.md) for safe listeners,
network boundaries, credentials and protected administrative metrics.

## Root keys and rotation

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

## Principals and authorization

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

## Transactional audit

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
[transactional audit runbook](operations/transactional-audit.md) for guarantees,
failure behavior and operational tests.

## Audit retention and export

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

## Complete database recovery

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
[the recovery runbook](operations/backup-restore.md) for rotation, deployment
configuration, version compatibility, and client resynchronization.

## Tenant and secret-path scopes

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
