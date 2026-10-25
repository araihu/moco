# Full backup and recovery

`moco-backup` provides a full database snapshot, structural verification, and
verified restoration to a new offline path. Build it from the same Mocó release
used by the server:

```bash
go build -o ./moco-backup ./cmd/moco-backup
```

## What is recovered

The archive contains all SQLite tables and their schema: tenants, vaults,
wrapped data keys, encrypted secrets and metadata, authorization policies and
revision, audit ledger and sequences, idempotency responses, insertion
sequences, global and tenant watch checkpoints (including tombstones), and the
encryption root-key ID and epoch. Secret values remain envelope encrypted.

The source is opened read-only and is never created or migrated by the backup
command. `VACUUM INTO` creates a transactionally consistent snapshot of the live
database, including committed WAL data. Uncommitted transactions and mutations
after the snapshot boundary are excluded. Metadata is inspected on the isolated
snapshot, so a concurrent rotation cannot mix key-era counts from different
database states. No shell SQLite binary or external service is required.

The v1 archive contains exactly `manifest.json` followed by `database.sqlite`.
The manifest records the format, creation time, database length and SHA-256,
schema version, watch revision, active encryption epoch, and sorted required
root-key IDs. IDs include every stored wrapped key and the active deployment
era. `createdAt` marks the start of the snapshot operation, a conservative data
freshness boundary rather than the completion time of a potentially long copy.
It contains no root-key material. SHA-256 detects accidental corruption;
it does not authenticate an archive replaced by an attacker. Store backups in
access-controlled storage with appropriate encryption and tamper protection.
Paths, labels, policies, and audit metadata inside the SQLite snapshot are
readable, even though secret values are encrypted.

## Separate recovery custody

Maintain a protected, independently recoverable copy of:

- The root-key material for every era required by retained backups. The manifest
  identifies the required IDs. Finishing live rotation does not remove older
  backups' dependency on retired keys.
- Current principal/token-digest configuration (`MOCO_AUTH_CONFIG`), credentials
  used by clients, and the cursor HMAC key (needed to preserve audit path-digest
  correlation). These are not stored in SQLite. New cursor keys invalidate old
  cursors and change audit path digests.
- TLS certificates/private keys or a way to reissue them, plus deployment
  settings and the matching server/backup binaries.

The recovery keyring is a regular JSON file with no group/world permissions,
using the `MOCO_ENCRYPTION_KEYS` shape:

```json
{
  "activeKeyId": "root-v2",
  "keys": {
    "root-v1": "<standard-base64 32-byte historical key>",
    "root-v2": "<standard-base64 32-byte current key>"
  }
}
```

Provision this file through the deployment's protected key-custody mechanism.
Use mode `0600` and keep it outside the backup archive and backup destination.
For legacy single-key deployments, represent the original key and its exact ID
in this same keyring format. Keys are never supplied as command-line values or
printed in command results.

## Create and verify

```bash
./moco-backup create --database /var/lib/moco/moco.db \
  --output /backups/moco-20261025T120000Z.tar --timeout 10m

./moco-backup verify --backup /backups/moco-20261025T120000Z.tar

./moco-backup verify --backup /backups/moco-20261025T120000Z.tar \
  --keyring /secure/recovery-keys.json
```

`create` defaults the database to `MOCO_DB_PATH`, then `./moco.db`.
`verify` without a keyring checks archive structure, size, checksum, clean
supported migration version, SQLite integrity, foreign keys, and manifest state.
Supplying a keyring additionally authenticates every wrapped vault key,
including keys in empty vaults, decrypts every secret in bounded row memory,
and verifies its plaintext digest. Plaintext is wiped after checking and is
never persisted or printed.

The command prints the manifest as JSON to stdout on success. Failures produce
a nonzero exit status and diagnostics on stderr. Every command has a positive
`--timeout` (default ten minutes) and honors interrupt/termination cancellation.
Archives and restored databases use `0600`; staging directories use `0700`.
Publication syncs the file and parent directory and uses a same-filesystem hard
link to avoid replacing an existing file, even if another writer races to
create the destination. The destination filesystem must support hard links.
Temporary files are removed on success, error, or handled cancellation.
An uncatchable kill/power loss can leave a private `.moco-*` staging directory;
remove abandoned staging directories after confirming no operation uses them.
If directory sync fails after publication, the command reports failure and
leaves the published file for inspection; it never replaces it on retry.

Backups scan the full database and use local staging space roughly equal to a
snapshot plus its uncompressed archive. Verification and restoration also need
space for a complete extracted database. Long reads can increase WAL retention;
monitor disk use and schedule backups for the deployment's workload. Choose
backup frequency and off-host retention according to the maximum acceptable
data loss, and alert on failed or overdue backups. Scheduling and remote
transfer are deployment responsibilities.

## Restore after losing a host

1. Isolate the old deployment and prepare a new private data directory. Keep
   the recovering server stopped and its network origin closed to clients.
2. Recover the archive, the separate keyring and current deployment config.
   Restore using the release with the same schema version as the backup.
   Dirty, older and newer schema versions are rejected; perform upgrades only
   after recovering and checking the matching release. No migrations run
   during restore.
3. Restore to a path that does not exist:

   ```bash
   ./moco-backup restore --backup /backups/moco-20261025T120000Z.tar \
     --database /var/lib/moco-recovery/moco.db \
     --keyring /secure/recovery-keys.json
   ```

   The command rejects any existing destination, symlink, or `-wal`, `-shm`,
   `-journal` sidecar. It verifies the entire archive and decryptability before
   publishing any database at the final path. A failed validation leaves no
   final database. It never overwrites an active or existing database.
4. Review recovered authorization policies against subsequent revocations.
   Restore current auth configuration/credentials and supply the keyring,
   cursor key, TLS configuration, and `MOCO_DB_PATH` through deployment secrets.
   Start with the intended active root-key ID and an epoch compatible with the
   restored state. To advance to a newer active era, use an epoch greater than
   the snapshot's epoch and fence all writers through the existing rollout
   procedure. Restore preserves the original epoch and wrapped keys.
5. Check `/readyz`, authenticated secret reads, and cross-tenant denials. If
   historical keys remain, complete fresh rotation sweeps and check internal
   encryption status before retiring them from the live keyring. Preserve
   those historical keys in backup custody until all dependent backups expire.
6. Force controllers to discard watch/cursor checkpoints and relist against
   the restored service. Restoring an older snapshot rolls back revisions and
   audit history; checkpoints from the lost timeline cannot establish
   convergence. Reconcile any writes that occurred after the backup boundary.
7. Reopen client access and resume scheduled backups, exports and monitoring.

## Recovery test, including rotation

```bash
GOWORK=off go test ./internal/adapters/backup \
  -run TestRecoveryBeforeDuringAndAfterRootKeyRotation -count=1 -v
GOWORK=off go test ./cmd/moco-backup ./internal/adapters/backup -count=1
```

The recovery test captures backups before rotation, with a mixture of old/new
vault-key eras, and after completed rotation. It mutates the live service and
removes the original database before restoring each backup. Pre-rotation and
mixed-era recovery fail without the historical root key. Successful recovery
checks tenants, vaults, plaintext values and ETags, policy snapshots, tenant
isolation through authenticated HTTP, creation-idempotency replay, watch state,
audit history, and continued writes. It then finishes rotation on the restored
database and verifies that current key material alone can decrypt it.

Additional tests cover concurrent writes, wrong key bytes, empty-vault key
authentication, missing vault keys, tampered ciphertext even with a recomputed
archive checksum, schema/foreign-key damage, malformed archives, unsafe tar
entries, insecure permissions, existing files/sidecars, and cancellation cleanup.
The commands are exercised end to end through their argument parser.

Run regular deployment recovery drills with actual off-host backups and
separately recovered key custody, and record elapsed recovery time and the
backup boundary. Repository tests do not verify the deployment's storage,
credential custody, scheduling, or network configuration.
