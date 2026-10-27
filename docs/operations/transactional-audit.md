# Audit and write failures

Moco saves every successful API change and its audit event in the same database
transaction. If the event cannot be saved, the change fails with `503`. This
covers tenant, vault, secret, permission, retention and key-rotation changes.
It is enabled automatically; there is no separate audit service to configure.

Audit records identify the principal, request, route and result. They do not
contain secret values, request bodies or bearer tokens. Secret paths are
represented by keyed digests. Read and rejected-request records are recorded
separately and can be missing if audit storage fails.

## Read the audit log

Use a principal explicitly allowed to `GET /internal/v1/audit` and connect to
the private admin listener. Set `MOCO_ADMIN_URL` to that listener, for example
`https://localhost:9443` through a local connection or secured tunnel:

```sh
curl --fail-with-body --cacert /private/moco/ca.pem \
  -H "Authorization: Bearer $MOCO_ADMIN_TOKEN" \
  "$MOCO_ADMIN_URL/internal/v1/audit?afterSequence=0&limit=100"
```

Read the events in sequence order and continue from the last returned sequence.
This log is for investigation. Use watch/relist for resource synchronization,
not audit polling. The [API reference](https://moco.araihu.com/openapi/)
describes the response and retention parameters.

## Export or expire records

The released server image includes `/moco-audit-export`. On a host where the
matching binary and database are available:

```sh
moco-audit-export --database /var/lib/moco/moco.db \
  --output /private/audit.jsonl --manifest /private/audit.manifest.json
moco-audit-export --verify \
  --output /private/audit.jsonl --manifest /private/audit.manifest.json
```

The destination must be new. The exporter reads the database without changing
it and writes private files. Keep exported metadata protected. To purge old
records, first export what you need, then preview
`POST /internal/v1/audit/retention` with `dryRun=true`. Deletion requires a cutoff
at least one hour old; repeat the same cutoff until `complete` is true and
`remaining` is zero. The principal needs an explicit retention permission.

## Respond to an audit failure

1. Check readiness, logs, disk space and storage permissions. Monitor
   `moco_audit_failures_total` and the `MocoAuditPersistenceFailure` alert.
2. Repair the storage problem before restarting. In production, an audit failure
   keeps readiness false until restart.
3. Verify readiness and an authorized write after restarting, then confirm a
   fresh [backup](backup-restore.md).

A connection failure does not prove that a change was rolled back: the server
may have committed before the response was lost. Retry tenant/vault creation
with the original idempotency key. For other writes, read the current state and
use ETags before retrying.

The ledger is stored in SQLite and can be changed by database administrators or
explicit retention. Keep protected off-host backups and log shipping when you
need an independent investigation record.
