# Transactional audit

Every successful protected HTTP mutation commits its business state and one
success audit event in the **same SQLite transaction**, before any success
headers or response body are released. This is enabled in both development
and production server profiles. An unavailable or nontransactional auditor
cannot silently allow writes: the endpoint returns `503` without running the
mutation.

The boundary covers tenant/vault create, replace and delete (including cascades),
secret create/replace/delete, vault-key creation, idempotency claims and expiry
cleanup, public/tenant watch revisions and tombstones, authorization replacement,
audit retention and the whole bounded key-rotation batch. A failed final audit
insert or commit rolls back them all. Repository operations use savepoints inside
the request transaction; their local commits cannot make an unaudited partial
request durable. Ordinary repository reads and precondition checks use the same
transaction context, without exposing it to other requests or the policy reloader.

Before listening, the server commits its epoch verification/adoption and policy
bootstrap with a synthetic `STARTUP /internal/startup` success event, principal
`system:startup`, and a generated request ID. If that event cannot commit, startup
fails and epoch/policy changes are rolled back. Root-key authentication happens
first, so bad keys cannot change the epoch. Schema installation/migrations run
before this bootstrap; local schema maintenance, raw administrator SQL and
offline restore remain privileged operator actions outside the HTTP guarantee.

## Commit, cancellation and responses

Writes use WAL with explicit `synchronous=FULL`. SQLite must run on the supported
local filesystem with functioning locks, flushes and persistent storage; this
does not make unsupported network SQLite storage safe. Backup mirrors stay
separate from the live SQLite volume.

Each HTTP mutation has a 20-second transaction context and a bounded 4 MiB
response buffer. Existing mutation contracts fit this limit; watch/read
responses keep their streaming behavior. Handler errors, cancellation, panics
and response serialization/size failures cannot leave partial mutation state
or a false success event. Failed commits retire their database connection before
another request can use it. The response buffer is discarded on an audit/commit
failure and the server returns `503` with `Retry-After: 1`, without an uncommitted
ETag, Location or secret metadata body.

Authorization reload notifications are deferred until the audited commit. A
rolled-back policy never emits a notification. If publication fails **after**
commit, the server logs the notification error and retains the success response:
data and its audit are already durable, and repository polling still allows
reload convergence. A transaction context must not escape its synchronous
callback or be used by a repository from a different store.

If the process dies after commit but before response delivery, **both** data and
audit may be present even though the client observed a timeout. Likewise a
commit I/O error can have an uncertain client-visible outcome; database atomicity
keeps business data and its audit together. Use the original `Idempotency-Key`
when retrying tenant/vault creation, and ETags/conditional operations plus a
read/relist for reconciliation. Do not infer "nothing happened" from a broken
connection or retry unconditional writes blindly. Restoring a backup still
requires discarding watch/cursor checkpoints from the lost timeline.

## Attempts and incident handling

Successful mutations produce exactly one request success event, using the final
HTTP status, authenticated principal, request ID and HMAC of a logical secret
path/prefix. No plaintext secret value, request body, query or bearer token is
recorded. An idempotent replay gets its own attempt event while replaying the
original resource and ETag.

Business errors roll back the request and then use independent attempt logging.
Read-only API requests also retain post-response audit. These read/rejection
events can be missing if audit storage fails; they do not imply an unaudited
business mutation. Reads of the internal audit endpoint are excluded to avoid
self-generated polling loops. Retention can later remove committed events
according to the existing explicit administrative policy; the retention mutation
itself cannot commit without its new audit event.

Audit failures emit logs and increment `moco_audit_failures_total`; production
readiness latches false until restart, and `MocoAuditPersistenceFailure` alerts.
Investigate storage and inspect any independent read/attempt gap before restarting.
A startup audit failure exposes no listener and is detected as unavailability.
This is a local transactional ledger, not an immutable or externally attested
log: root/database administrators and explicit retention can modify its history.
Independent key custody, verified mirrored backups and protected log shipping
remain part of the production operations runbook.

## Verification

```sh
make check
go test -race ./internal/adapters/db ./internal/adapters/http ./cmd/moco-server
make image-check IMAGE=moco:$(git rev-parse --short HEAD)
make monitoring-check
```

Fault-injection tests reject audit inserts and introduce a deferred foreign-key
constraint that fails only at final commit. They compare all business tables,
including ciphertext, idempotency and watch state, and require `503` without
success headers. Removing the fault must allow the same idempotency key to retry
successfully and then replay unchanged. A commit barrier verifies that another
SQLite connection and the response writer cannot see the mutation early.

Additional tests cover complete rotation-batch rollback, retention rollback,
commit-before-notification, suppressed notifications on rollback, post-commit
notification errors, mixed-store rejection, missing auditors, cancellation,
panic, response encoding/size failures and reopen-after-commit durability.

The executable operational drill injects real SQLite audit faults into the
running production server: failed secret writes preserve values/ETags, failed
policy writes preserve revision/content, readiness reports the incident, and a
failed startup cannot advance the epoch. It then repeats the existing automatic
backup, crash/restart, rotation and mirrored recovery checks. Image CI runs the
same drill against executables extracted from the OCI image.
