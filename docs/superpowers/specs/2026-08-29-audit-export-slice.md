# Offline Audit Export Slice

## Scope

This slice adds `moco-audit-export`, a local command for backing up the request
audit ledger as newline-delimited JSON (JSONL). It uses only the existing
SQLite adapter, the Go standard library, and a deployment-owned filesystem.
There is no HTTP export operation, network exporter, KMS/HSM integration, or
new runtime dependency.

## Contract

The command accepts a database path, a required output path, and an optional
exclusive `--after-sequence` checkpoint. It captures the current highest audit
sequence before reading and exports rows through that sequence in ascending
order. Each line is one audit metadata object using the same camel-case field
names as the internal audit API. Optional principal and keyed path-digest
fields are omitted when absent; request bodies, query strings, credentials,
plaintext secret paths, ciphertext, and key material are never exported.

The database is opened read-only and is never migrated by the command. Rows
appended after the captured upper bound are intentionally left for a later
run. Rows removed concurrently may be absent; callers can resume with the
last exported sequence and compare the resulting files with their operational
backup policy.

## Filesystem safety

File exports are streamed to a same-directory temporary file, restricted to
mode `0600`, synced, and published without replacing an existing destination.
Failures remove the temporary file. `--output -` is an explicit opt-in stream
to stdout and cannot provide the atomic-file guarantee; the caller owns pipe
permissions and durability in that mode.

The command emits its summary only on stderr so stdout remains valid JSONL.
The exporter is read-only and does not alter audit sequences or the public
resource-watch revision.

When `--manifest` is supplied, a second private JSON file records the format,
exclusive start sequence, captured upper sequence, last exported sequence,
count, completion flag, and SHA-256 of the exact JSONL bytes. `--verify`
requires both paths and performs a streaming checksum, schema, ordering, and
sequence-bound check without opening SQLite. The manifest and JSONL are both
published without overwriting existing destinations; they are committed in
sequence, so an operator should treat a failed manifest publication as an
incomplete export and rerun with fresh paths.
