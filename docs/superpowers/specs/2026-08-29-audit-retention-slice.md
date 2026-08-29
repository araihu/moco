# Audit Retention Slice

## Scope

This slice adds bounded local retention for the internal request-audit ledger.
It uses only SQLite and the existing internal HTTP boundary; no external
storage, KMS/HSM, network exporter, or new runtime dependency is introduced.
The existing audit list and public resource APIs remain unchanged.

## Contract

`POST /internal/v1/audit/retention?before=<RFC3339>&limit=<1..200>` deletes at
most one page of rows whose `occurred_at` is strictly older than `before`. The
cutoff must not be in the future. Omitting `limit` uses the bounded default of
50. The response contains only the normalized cutoff, deleted count, current
remaining count, `hasMore`, and `complete`; it contains no audit event data.

Callers repeat the same cutoff until `complete=true` and `remaining=0`. The
count is not a snapshot: concurrent insertion of an event before the cutoff
can require another bounded request. Sequence numbers remain monotonic and
deleting audit rows never advances the public resource-watch revision.

The endpoint requires an exact Casbin `POST` permission and should be exposed
only from a restricted deployment origin. Audit middleware records the
retention request itself after the response; the endpoint does not recursively
record audit reads.

## Safety invariants

Deletion is ordered by `(occurred_at, sequence)` and limited in the SQL
statement, so one request has a predictable transaction cost. The cutoff is
strict, preserving events exactly at the boundary. Export remains a separate
offline slice; operators should export or back up retained data before running
retention.
