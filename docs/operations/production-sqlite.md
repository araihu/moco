# Single-instance production profile

This profile runs one Mocó writer and a backup worker on a Linux Docker host.
SQLite, its WAL and shared-memory file must stay on the same persistent **local**
filesystem. Do not use NFS/CIFS for `/data`, scale the server, or share this data
directory between hosts. The mirror may use a separately mounted off-host
filesystem. This profile provides recovery, not high availability.

## Release image and runtime

For the published v0.0.3 release, use the verified multi-platform image index:

```sh
export MOCO_IMAGE=ghcr.io/araihu/moco@sha256:d6a8167c48c771884729405ff4a0d3c8f65bb1b69bbcb96c091745de6eade180
docker pull "$MOCO_IMAGE"
```

See [release evidence](../releases/0.0.3.md) and verify your recovery prerequisites
before deploying. Building from source is optional; for a reviewed custom build:

```sh
make check
make image-check IMAGE=moco:$(git rev-parse --short HEAD)
make monitoring-check
```

The Dockerfile pins the Go 1.27.3 builder by manifest digest. Its final OCI image
uses `scratch`, static executables, a CA bundle and UID/GID `65532`; it contains
no shell or package manager. The version and source revision are OCI labels,
and the server reports the embedded version. `make image` builds locally.
SemVer tag pushes automatically publish multi-platform images and pinned
Helm charts to GHCR after validation; see [releases.md](releases.md). Deployment
remains an operator action. You can also publish a reviewed local build to your
own registry. Record its digest and use that digest in `MOCO_IMAGE`.
Release builders should rebuild this pin when updating Go, run the required
checks, and retain the matching binaries/image with backups.

`deploy/production/compose.yaml` limits CPU, memory and PIDs, drops all Linux
capabilities, sets `no-new-privileges`, mounts the root filesystem read-only,
rotates JSON logs through Docker's local driver, and bounds writable temporary
storage. The server has a 35-second graceful shutdown window for outstanding
watch polls; Compose grants 40 seconds. SQLite and credential directories are
0700, private files are 0600, and the server sets umask 0077. A host file lock
rejects a second production writer using the same data directory.

## Provision private storage and credentials

Install Docker Engine, Compose, and a host firewall. Before deploying, provision
an access-controlled off-host filesystem, its transport/storage encryption and
mount monitoring. Provision Linux directories with the container's numeric UID:

```sh
sudo install -d -o 65532 -g 65532 -m 0700 /srv/moco/data /srv/moco/backups /etc/moco
# Mount /mnt/moco-offhost using your protected storage service first.
mountpoint -q /mnt/moco-offhost
sudo install -d -o 65532 -g 65532 -m 0700 /mnt/moco-offhost/archives
```

Set `MOCO_IMAGE` to the published digest above or your reviewed custom digest:

```sh
# MOCO_IMAGE is the immutable server image selected above.
docker run --rm --network none --read-only \
  --mount type=bind,src=/etc/moco,dst=/bootstrap \
  --entrypoint /moco-ops "$MOCO_IMAGE" init --directory /bootstrap/credentials

docker run --rm --network none --read-only \
  --mount type=bind,src=/mnt/moco-offhost/archives,dst=/mirror \
  --entrypoint /moco-ops "$MOCO_IMAGE" init-mirror
```

Save the printed destination UUID as `MOCO_MIRROR_ID`. The worker requires the
matching private `.moco-destination` marker on every job. This detects an absent
or wrong mounted destination instead of silently using an unmarked local
mountpoint. The identity does not prove geographic separation: the operator
must provision the actual remote storage, availability and access controls.
Do not copy the marker into the underlying unmounted host directory.

`init` refuses an existing directory. It generates independent random root,
cursor, metrics and bootstrap administrator credentials. `access.json` contains
only token digests. `admin-token` is for initial administration; the Compose
server does not mount it. Store it in protected administrator custody. Generate
separate principals for applications, allow only their tenant/vault/secret-path
prefix and HTTP methods, and never distribute the bootstrap administrator or
metrics token to applications. Principals/digests come from `access.json` on
each startup; policy snapshots are authoritative in SQLite after first boot.
Update policies through the private, authenticated authorization API with
`If-Match`, including revocation checks after recovery.

Provision `tls.crt`, `tls.key` and `ca.pem` into the credentials directory:

- The server certificate must cover the public hostname and `localhost` (used
  for private probes/monitoring); a private issuing CA supports both names.
- `tls.crt` holds the leaf and intermediate chain, `tls.key` its matching PEM
  private key, and `ca.pem` the issuing root/intermediate trust bundle.
- Set owner `65532:65532` and permissions 0600. The host path is private; do not
  put credential material in Git, `.env`, command arguments or image layers.
- Issue/renew through your certificate service. Restart after replacing any
  mounted credential/certificate files. Check certificate expiry externally.

Maintain an independently recoverable copy of root keys **for every era used by
retained backups**, current principal credentials/digests, cursor key, TLS
issuance material and deployment settings. Archive storage never receives root
keys. The backup worker can decrypt all secrets for verification and therefore
has equivalent key custody sensitivity to the server, despite having no network
and a read-only data mount. Protect the Docker host and daemon accordingly.

## Start and verify

Copy `deploy/production/env.example` to a protected operator configuration path,
fill in actual image/storage settings and the mirror UUID, then run:

```sh
docker compose --env-file /etc/moco/production.env \
  -f deploy/production/compose.yaml config --quiet
docker compose --env-file /etc/moco/production.env \
  -f deploy/production/compose.yaml up -d --wait --wait-timeout 900
```

The profile requires real bind sources; Compose must not create missing host
paths. Production startup rejects missing TLS, legacy shared-token mode,
inline root/cursor/metrics secrets, public credential permissions and relative
database paths. The server authenticates every existing wrapped vault key
before listening, including empty vaults, and checks the configured encryption
epoch. Wrong or missing historical key bytes fail startup before epoch changes.

Public HTTPS is port 8443. Administration and authenticated `/metrics` use a
separate HTTPS port, 9443, published **only on host loopback**. `/internal/*` and
`/metrics` return 404 on the public listener even with administrator credentials.
Use an SSH tunnel or a host-local administrative client for internal operations.
Keep the Docker network private and never publish container port 9443 on a
public interface. `/livez` and `/readyz` remain unauthenticated process probes.
The readiness check covers SQLite, active key ID/epoch and the audit health
latch. Readiness is not a load-balancer configuration; configure your ingress
to honor it if present. Docker health status alone does not restart a process
that is still alive but unhealthy.

After startup, verify authenticated reads/writes, application cross-tenant
denials, public admin-path isolation, health checks and a successful mirrored
backup. Check `docker compose logs` for structured startup, request and backup
events. Request logs omit URL/query values and principal/token/secret values;
metrics labels are bounded route groups, methods and statuses. Protect and ship
logs using your existing logging service; Docker rotation alone is local.

## Backups, retention and recovery objectives

The worker immediately backs up and then starts one job every 15 minutes,
without overlapping jobs. Each job has a ten-minute deadline. It creates a
consistent live SQLite snapshot, authenticates every vault key and secret,
copies the archive to the marked mirror and verifies the copied archive with
the keyring. Only a fully successful job advances `lastSuccess` and `snapshotAt`
in `/backups/status.json`. Snapshot freshness uses the transaction's conservative
start boundary. Interrupted/failed jobs leave prior successful archives and
freshness intact, increment failures when status storage is writable, report a
nonzero one-shot exit and emit errors from the daemon. A job-level file lock
prevents another worker/one-shot job from pruning concurrently.

Both destinations retain the most recent 96 managed archives by default
(approximately 24 hours at the normal cadence; manual backups also consume
retention). `MOCO_BACKUP_KEEP` sets a count, minimum two. Pruning runs only after
successful verification of both copies, leaves unrelated files alone, and
rejects unsafe files in the managed set. Never put unrelated archives under
the managed naming convention. Mirror publication requires file/directory
sync and hard links on that destination filesystem. Object-store shipping,
longer-term retention and immutable retention policies can be layered onto
this protected destination by the storage operator.

Size `/backups` for retained uncompressed archives plus staging: at peak allow
roughly three additional full database sizes, plus source WAL growth. The
worker sets `TMPDIR=/backups` so full-database verification does not exhaust
the small `/tmp` tmpfs. The mirror needs retained archives plus a temporary
copy and extraction resources on the worker. A kill/power failure can leave
private `.moco-*` staging directories; remove only confirmed abandoned stages.
Backups contain readable metadata; use encrypted, access-controlled storage.

Initial operational targets are **RPO 30 minutes** (alarm threshold on the last
verified mirrored snapshot) and **RTO 15 minutes**. These are acceptance targets,
not availability guarantees. Measure them with your database size, network and
independent key recovery. A 15-minute schedule plus a slow ten-minute job can
consume most of the RPO budget; increase resources or frequency if needed.

Run a one-shot job with the same configuration:

```sh
docker compose --env-file /etc/moco/production.env \
  -f deploy/production/compose.yaml run --rm --no-deps backup \
  backup-once --database /data/moco.db --keep 96
```

## Observability and response

Use `deploy/production/prometheus.yaml` and `alerts.yaml` with your monitoring
service. Give the Prometheus service account separate read-only copies of the
metrics token and CA, never root keys, and set the actual TLS server name. A
host-local Prometheus reaches `https://localhost:9443/metrics`; remote monitoring
needs an explicitly secured private route/tunnel. Configure a real Alertmanager
receiver and test notification delivery. The example does not contact a
notification service or invent an on-call destination.

Metrics include request totals and latency histograms, in-flight requests,
runtime heap/goroutines, readiness, data/backup filesystem free space, durable
audit failure detection, backup status readability, last verified mirror
snapshot, failures and job duration. Watch polls use a separate group so their
expected 25-second latency does not raise ordinary API latency alarms.
Alerts cover unavailable/not-ready, audit persistence errors, missing/failed or
overdue backups, low disks, API errors and API p95 latency. Scrape errors also
detect server/host loss. Monitor the mirror mount, host clock, certificate
expiry and host health in your infrastructure monitoring.

Successful mutations and their audit event now commit in **one SQLite
transaction before the response**. Audit/commit failure rolls back the complete
request and returns `503`; no success ETag, Location or body is released. Startup
epoch/policy initialization is audited too. The production server logs incidents,
increments metrics and latches readiness false until restart. Investigate storage
before restarting. Read-only and rejected-attempt audit remains post-response;
failures there can leave a read/attempt gap, but cannot produce an unaudited
business mutation. See [transactional-audit.md](transactional-audit.md) for the
precise scope, client retry rules and fault-injection tests.

## Upgrade, interruption and recovery drills

1. Check readiness, a fresh verified mirrored backup and independent key/config
   custody. Record image digest, schema, epoch, snapshot boundary and last client
   revision. Test the candidate against a recovery copy before production.
2. Stop the single server gracefully, retain the old image and deploy the new
   digest. Startup migrations run automatically. Verify health, authenticated
   reads/writes, least-privilege denials and a fresh backup before reopening the
   client origin. For incompatible schema changes, rollback requires restoring
   the old snapshot with its matching release; do not run an old binary on a
   migrated database or assume that changing an image reverses migrations.
3. For recovery, isolate the old server, prepare a new private local data
   directory, and follow [backup-restore.md](backup-restore.md). Use the matching
   backup release; restore from the **mirror**, with separately recovered keys,
   to an absent offline `/data/moco.db`. Never overwrite a live database. The
   image contains `/moco-backup` for create/verify/restore and `/moco-audit-export`
   for audit snapshots. Mount restored data read-write, the archive and keyring
   read-only, and a sized private temporary volume; override the entrypoint with
   `/moco-backup`. Do not accidentally start `/moco-server` before restore.
4. Review policy revocations after the snapshot, epoch compatibility, plaintext
   reads/ETags, cross-tenant denials and continued writes. Relist clients and
   discard checkpoints/cursors from the lost timeline. Measure recovery elapsed
   time and actual snapshot data loss before restoring traffic.
5. During key rotation retain old eras, increment `MOCO_ENCRYPTION_KEY_EPOCH`,
   restart the single writer, run bounded admin rotation sweeps and verify zero
   remaining old keys. Back up and test restoration before retiring live keys;
   retain historical key material in independent custody until its last backup
   expires. The backup worker uses the replaced keyring after container
   recreation, so recreate both services after credential/key-file replacement.

`make operational-test` runs real executables through installation, TLS probes,
least-privilege tenant checks, graceful restart, automatic repeated backups,
mixed-era rotation, SIGKILL/restart, wrong-key startup, mirror failure detection,
loss of the original SQLite files and restoration from the mirror with historical
keys. It checks unchanged ETags, isolation and continued writes. It prints the
snapshot boundary and measured fixture recovery time. `make check` includes it.

`make image-check` executes this drill with binaries extracted from the built
image and additionally runs an OCI container with the profile's user/resource
restrictions, read-only root, TLS, private admin/metrics, health, restart,
read-only-source backup and separate-volume mirrored recovery checks. It requires a Linux Docker host/client of the same CPU
architecture; SSH Docker contexts are supported. `make monitoring-check` validates
the Prometheus rules and alarm behavior using a digest-pinned promtool container.
CI runs these image/monitoring gates alongside the Dagger check pipeline.

Repository fixtures do not verify your remote filesystem, notification receiver,
certificate renewal, workload capacity or independent key custody. Run recovery
drills in the target environment and a load/security review before releasing
real production secrets.
