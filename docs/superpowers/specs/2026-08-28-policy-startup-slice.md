# Authorization Policy Startup Slice

## Scope

The server composition root now connects configured bearer principals,
SQLite-backed authorization snapshots, and the in-process policy reloader. The
public API contract is unchanged. Restricted snapshot administration is covered
by the follow-up administration slice; distributed change transport remains
future work.

## Bootstrap and authority

`MOCO_AUTH_CONFIG` remains the source for principals and their SHA-256 token
digests. On the first configured start, the role bindings and policies are
validated as a complete Casbin candidate and persisted atomically. A metadata
row marks the snapshot initialized in the same transaction. An empty but valid
configuration is therefore an intentional default-deny snapshot.

On later starts, the initialized SQLite snapshot is authoritative for role
bindings and policies; those fields from the file are ignored. The file is
still required for the current principal keyring, and persisted bindings that
reference an unknown current principal fail startup rather than granting an
unusable or ambiguous identity.

The legacy `MOCO_BEARER_TOKEN` mode remains an in-memory full-access
compatibility path and does not create a persisted policy snapshot.

## Lifecycle

Configured startup creates a `MemoryPolicyChangesBus` and a
`PolicyReloader`. The reloader performs an initial authoritative load, then
reloads atomically after each committed change signal. Its context is tied to
the HTTP server's signal-driven shutdown. A load or validation failure is
reported to the composition root, which gracefully stops the listener and
returns an error instead of serving with a stale policy.

## Deferred work

Principal administration endpoints, distributed bus transport, audit records,
and token issuance/revocation are not part of this slice. The internal policy
writer uses the authorization application service so persistence commits before
reload signals are published.
