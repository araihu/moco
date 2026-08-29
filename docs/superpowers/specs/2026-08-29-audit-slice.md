# Request Audit Ledger Slice

## Contract boundary

The internal OpenAPI contract adds `GET /internal/v1/audit`. The public API
remains unchanged. The operation requires an explicit Casbin permission for the
exact internal path; the legacy bearer mode grants the compatibility principal
the same read permission as the existing authorization administration endpoint.

The endpoint returns an append-only sequence ordered by SQLite insertion. The
`afterSequence` query parameter is an exclusive checkpoint and
`nextAfterSequence` continues a page. The page size is bounded at 200 so an
operator can resume from a durable checkpoint without an opaque cursor secret.

## Data minimization

Protected API attempts are recorded after their HTTP response is produced,
including authentication, authorization, validation, not-found, and server
errors. Liveness, readiness, and reads from `/internal/v1/audit` are excluded;
excluding the reader prevents a tailing client from generating self-events. An
event contains only:

- the durable sequence and UTC occurrence time;
- the request ID, authenticated principal when available, method, and path
  without a query string;
- the final status code and a coarse `success`/`failure` outcome; and
- a keyed HMAC-SHA-256 digest for a decoded secret `path` or collection
  `prefix` query value when one was supplied.

Request bodies, query strings, bearer credentials, ciphertext, and plaintext
logical secret paths are never persisted or returned by the endpoint. Audit
events do not advance the public resource-watch checkpoint.

## Failure and lifecycle semantics

The response is committed before the audit append is attempted. A failed append
is logged with its request ID and does not replace or alter the response. This
keeps request availability independent from the audit store, but means the
ledger is best-effort rather than an atomic transaction with resource
mutations. Retention, export, tamper-evident shipping, and external storage
remain deployment-owned follow-up work.
