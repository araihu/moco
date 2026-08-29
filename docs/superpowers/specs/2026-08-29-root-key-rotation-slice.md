# Root-Key Rotation Slice

## Scope

This slice adds multi-era root-key reads and bounded online rewrapping of
persisted vault data keys. The public API shape and secret ciphertext remain
unchanged. Deployment configuration supplies the active key plus previous key
eras; a restricted internal operation migrates wrapped vault keys in keyset
pages.

## Configuration and key hierarchy

The legacy `MOCO_ENCRYPTION_KEY` plus `MOCO_ENCRYPTION_KEY_ID` form a one-entry
keyring for backwards compatibility. `MOCO_ENCRYPTION_KEYS` is an alternative
strict JSON document:

```json
{
  "activeKeyId": "root-v2",
  "keys": {
    "root-v1": "<standard-base64-32-byte-key>",
    "root-v2": "<standard-base64-32-byte-key>"
  }
}
```

Every key is exactly 256 bits and every ID is visible ASCII. The active ID must
be present. New vault keys use the active era; reads accept any configured era.
The server copies the keyring into the envelope adapter and clears the startup
configuration buffers immediately after construction. A retired era must stay
configured until rewrapping completes.

`MOCO_ENCRYPTION_KEY_EPOCH` is a positive, monotonically increasing deployment
epoch persisted in SQLite. A process adopts a newer epoch atomically at startup
and rejects a stale or mismatched configuration. Stale processes may continue
compatible reads when their keyring can unwrap the stored era, but their secret
writes and rotation requests are fenced. Every writer must therefore start with
the new active key and epoch before a rotation begins.

## Internal operation

`POST /internal/v1/encryption/rotation` scans at most 200 rows ordered by
`(tenant_id, vault_id)`. `afterTenantId` and `afterVaultId` are an exclusive
keyset checkpoint and must be supplied together. Each row already using the
active ID is skipped. Other rows are unwrapped and rewrapped in memory under
the active key; the data key never crosses the encryption adapter boundary.
The repository then performs a compare-and-swap update over the old root ID,
salt, and wrapped bytes. A concurrent winner is counted as skipped, making page
retries safe. The response reports `scanned`, `rewrapped`, `skipped`,
`hasMore`, the next checkpoint, the active epoch, and `remainingOldKeys`.
`complete` is true only when the current count of keys using an older root-key
ID is zero. Because the keyset pages are not a long-lived snapshot, callers
must follow the returned checkpoints and then run a fresh sweep until
`complete=true` and `remainingOldKeys=0`; new vault creation can make a later
sweep necessary.

The expected active ID and epoch are part of the persistence CAS itself. If the
shared state changes after the worker has listed a page, the replacement is
rejected before any row is changed. Secret writes, deletes, and first-use vault
key creation use the same in-transaction fence, so an old process cannot
reintroduce an older-era envelope after the rollout advances.

The operation never accepts or returns key material, plaintext, ciphertext, or
secret paths. It requires an exact Casbin `POST` policy for its internal route
and is intended for a dedicated deployment operator. The audit middleware
records the request metadata, while the operation itself is not a public
resource mutation.

## Resource and concurrency invariants

Rewrapping preserves the vault data key, so existing secret ciphertext remains
decryptable and its metadata, digest, ETag, and version do not change. The
`vault_keys` update trigger is removed in this migration so wrapped-key-only
maintenance does not advance the public `resourceVersion` watch checkpoint.
New vault-key insertion still participates in resource history. Multiple
rotation workers may run concurrently; compare-and-swap prevents stale pages
from overwriting a newer rewrap.

If an old key era is missing or cannot authenticate a wrapped key, the batch
fails without returning the affected identifier or key material. The caller
keeps the old era configured, repairs the deployment input, and retries the
same checkpoint.

## Deferred work

External KMS/HSM providers, automatic key discovery, scheduled rotation,
retired-key garbage collection, and audit retention/export remain deployment
or later product responsibilities. Root-key rotation is maintenance of
encryption material, not a reconciliation feed; controllers and operators still
use `/api/v1/watch` plus relist for public resource convergence.
