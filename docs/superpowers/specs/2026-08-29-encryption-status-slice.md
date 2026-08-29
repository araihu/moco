# Encryption Rotation Status Slice

## Scope

This slice adds a read-only internal status operation for the existing local
root-key rotation workflow. It uses only the SQLite store and the existing Go
HTTP boundary; it adds no KMS/HSM integration, network service, or runtime
dependency.

## Contract

`GET /internal/v1/encryption/status` requires its own exact Casbin `GET`
permission. It reports the authoritative shared active root-key ID, deployment
epoch, current count of vault keys whose stored root-key ID differs from that
active era, and `complete` when that count is zero. No key material, plaintext,
ciphertext, or secret path is accepted or returned.

The count is a point-in-time diagnostic, not a lock or a snapshot. Concurrent
writes and rotation workers can change it immediately. A caller must still use
the epoch fence on each mutating rotation request and repeat a fresh sweep
before removing an older key from deployment configuration.

The status endpoint remains useful when this process has a stale configured
keyring: it reads the shared active era without authorizing stale writes. A
missing rotation service returns `503`; storage failures return `500`.

## Operational flow

1. Roll out all writers with the intended active key ID and epoch.
2. Read status and execute bounded rotation pages using their keyset
   checkpoints.
3. Run a fresh sweep and read status again until `complete=true` and
   `remainingOldKeys=0`.
4. Only then remove the previous key era from deployment configuration, keeping
   a verified backup.
