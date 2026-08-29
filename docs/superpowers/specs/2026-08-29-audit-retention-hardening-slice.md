# Audit Retention Hardening Slice

## Scope

This slice adds local safety controls to the existing bounded audit-retention
operation. It uses only the Go standard library, SQLite, and the existing
internal HTTP boundary; no KMS/HSM, network service, or runtime dependency is
introduced.

## Safety contract

Destructive requests must use a cutoff at least one hour old. This buffer gives
an operator time to investigate and export records before deletion. Future and
more-recent cutoffs are rejected with `400`; the rule is enforced in the core
service as well as at the HTTP boundary.

`dryRun=true` performs the same cutoff-format, future-date, and limit
validation, but may inspect a more recent past cutoff. It returns the current
matching count without issuing a delete. Its `deleted` value is always zero and
its `dryRun` value is true. A dry-run response is informational: it must not be
treated as a convergence loop because repeating it does not change the ledger.

The existing delete path remains bounded to 200 rows, strictly before the
cutoff, and safe to repeat. Sequence allocation and the public resource-watch
revision remain unchanged. The count is still a non-snapshot diagnostic under
concurrent inserts or retention workers.
