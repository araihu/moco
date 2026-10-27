# Full backup and recovery

Use `moco-backup` to create a complete database snapshot, verify an archive and
restore into a new offline database. The [production profile](production-sqlite.md)
already schedules verified mirrored backups; use these commands for manual
checks and recovery.

## Get the recovery tools

The released server image includes both recovery binaries. On a Linux host
matching the image's CPU architecture, copy them without starting a server:

```sh
umask 077
mkdir -p /private/moco/bin
container=$(docker create ghcr.io/araihu/moco:0.0.3)
docker cp "$container":/moco-backup /private/moco/bin/moco-backup
docker cp "$container":/moco-audit-export /private/moco/bin/moco-audit-export
docker rm "$container"
export PATH="/private/moco/bin:$PATH"
```

Run them as an operator with access to the private files, using the same release
as the database. On other hosts, run the image with the corresponding tool as
its entrypoint and private data/archive/key mounts; do not start the server
entrypoint during restore.

## What is recovered

A backup contains tenants, vaults, encrypted values, permissions, audit history
and synchronization state. It includes committed data at the snapshot boundary,
not later writes. Creation can run while the server is serving requests.

The manifest records the snapshot time, schema version, checksum and required
root-key IDs. It contains no root-key material. Paths, labels, policies and audit
metadata remain readable; protect archives with access controls and encryption
at rest. Checksums detect corruption, not replacement by an attacker.

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
moco-backup create --database /var/lib/moco/moco.db \
  --output /backups/moco-20261025T120000Z.tar --timeout 10m

moco-backup verify --backup /backups/moco-20261025T120000Z.tar

moco-backup verify --backup /backups/moco-20261025T120000Z.tar \
  --keyring /secure/recovery-keys.json
```

Verification without a keyring checks archive structure, checksums, schema and
database integrity. Supplying the keyring also verifies that every vault key
and secret can be decrypted, without printing values. Success prints the
manifest; failure exits nonzero. Existing archive destinations are never overwritten.

Use private directories and mode `0600` for keys, archives and restored files.
The destination filesystem must support hard links and file/directory sync.
Allow space for a full snapshot, its archive, verification extraction and WAL
growth. Set a suitable `--timeout` for your database size. Schedule off-host
copies and alert on overdue or failed backups.

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
   moco-backup restore --backup /backups/moco-20261025T120000Z.tar \
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

## Rehearse recovery

Periodically restore an actual off-host backup into an isolated deployment with
separately recovered keys. Check a known value and ETag, an authorized write,
a denied cross-tenant request, and a fresh backup after startup. Include a
backup made before a root-key rotation so the drill exercises historical keys.
Record the snapshot's age and elapsed recovery time before reopening traffic.
