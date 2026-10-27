# Moco Helm chart

Deploy one Moco production writer with SQLite, native HTTPS, transactional audit
and verified local plus mirrored backups. The chart creates a StatefulSet, a
public ClusterIP Service, a headless Service, a tokenless ServiceAccount, and
NetworkPolicy. Optional resources are public Ingress and Prometheus Operator
ServiceMonitor with a separate private Service.

## Requirements and supported topology

- Kubernetes **1.32+**; CI tests Helm **3.22.1 and 4.3.1**.
- An enforcing NetworkPolicy CNI and Linux nodes supporting UID/GID 65532,
  RuntimeDefault seccomp and read-only root filesystems.
- A compatible production image (published **v0.0.3** or a reviewed source build) with `/moco-ops prepare-runtime` and
  `backup-loop --wait-ready`. Use a SemVer release image/chart or publish your
  own reviewed build. Release charts carry the immutable image digest; source
  chart defaults require an explicit image pin.
- Data and local backups on distinct filesystem PVCs. SQLite and all its WAL,
  SHM and journal files must remain on one local filesystem. Never use NFS/CIFS
  for the data claim. A StorageClass/PV must enforce the correct node topology;
  setting a value named `local` does not verify the storage implementation.
- A separately provisioned mirror PVC backed by an independent off-host
  filesystem supporting hard links and file/directory sync. The mirror may use
  network storage; SQLite data may not. Volume roots must permit UID/GID 65532
  to create a private child directory. CSI must support the configured access
  mode and group ownership handling.
- Seven credential files in an existing Secret and a certificate covering
  **localhost** plus the public hostname. No credential bytes belong in values.

This is recovery for one writer, with downtime during updates and node loss.
`replicaCount` is fixed at one. `ReadWriteOncePod` is the default; use
`ReadWriteOnce` only with a storage driver that cannot support RWOP and an
operational guarantee against duplicate writers. Never force-delete a running
pod, bypass CSI fencing, scale the StatefulSet or start a second release on its
data. The process's local file lock is an additional check, not distributed
fencing. See the Kubernetes [StatefulSet/storage documentation](https://kubernetes.io/docs/concepts/workloads/controllers/statefulset/).

## 1. Select the published image or build your own

Published SemVer releases provide `ghcr.io/araihu/moco` and
`oci://ghcr.io/araihu/charts/moco`. Their chart defaults pin the matching image
digest. See the [release runbook](https://github.com/araihu/moco/blob/main/docs/operations/releases.md)
for version selection, package visibility, checksums and OCI installation.
For those packages omit `image` from operator values. The source-chart procedure
below remains available for local/custom image builds.

From the repository root:

```sh
make check
make image-check IMAGE=registry.example.com/team/moco:RELEASE
docker push registry.example.com/team/moco:RELEASE
# Record the registry's sha256 digest for that reviewed image.
```

Set `image.repository` and `image.digest`. The image's default entrypoint is the
server, but the chart explicitly selects server/operations commands for each
container. The same digest is used for initialization, serving and backups.

## 2. Provision the mirror independently

Create the mirror PVC in the target namespace outside this release. Provision
its dedicated `moco/` directory with owner 65532:65532 and mode 0700 on the
filesystem backing that claim. As a storage administrator, initialize its marker
once using this release image, for example on a host that mounts that filesystem:

```sh
sudo install -d -o 65532 -g 65532 -m 0700 /mnt/moco-offhost/moco
docker run --rm --network none --read-only \
  --mount type=bind,src=/mnt/moco-offhost,dst=/mirror \
  --entrypoint /moco-ops registry.example.com/team/moco@sha256:REPLACE \
  init-mirror --directory /mirror/moco
```

Record the printed UUID as `backups.mirrorID`. Do not create the marker in an
unmounted fallback directory or copy it into an unrelated PVC. The init
container verifies the existing marker and refuses an absent/wrong destination.
It never generates the marker. The UUID identifies a destination; the operator
must establish its actual off-host independence, encryption and availability.

## 3. Provision credentials and certificates

Use `/moco-ops init --directory NEW_DIRECTORY` from the release image to generate
the root keyring, cursor key, metrics token and access policy. When generating
on a workstation with a bind mount, override the bootstrap container's user to
the workstation UID/GID or provision its writable directory for UID 65532.
Keep `admin-token` separately in administrator custody. Obtain TLS material from
your certificate service, then create an existing namespaced Secret:

```sh
kubectl create namespace moco
kubectl -n moco create secret generic moco-credentials \
  --from-file=access.json=/private/moco/access.json \
  --from-file=cursor-key=/private/moco/cursor-key \
  --from-file=keyring.json=/private/moco/keyring.json \
  --from-file=metrics-token=/private/moco/metrics-token \
  --from-file=tls.crt=/private/moco/tls.crt \
  --from-file=tls.key=/private/moco/tls.key \
  --from-file=ca.pem=/private/moco/ca.pem
```

`tls.crt` contains the leaf/intermediate chain, `tls.key` the matching PEM key,
and `ca.pem` the trusted issuing CA. Kubernetes Secret API encryption, RBAC,
external credential custody and certificate renewal remain operator-managed.
Back up historical root keys independently of the cluster and archive storage.

The non-root init container reads only these seven projected keys, accepting
Kubernetes' internal projection links within that volume. It creates private,
regular 0600 copies in memory-backed storage and 0700 application directories
under the mounted PVC roots. It restores private modes widened by CSI fsGroup,
rejects storage symlinks/special files, and leaves database/archive bytes intact.
The server keeps its strict regular-file/permission checks. Source Secret
projection is mounted only in the init container. The backup container mounts
only the copied keyring and CA, and mounts live data read-only. See Kubernetes
[volume security contexts](https://kubernetes.io/docs/tasks/configure-pod-container/security-context/).

Changing an existing Secret does **not** update these private copies. Bump
`credentials.revision` and upgrade the release to restart the pod and recopy
credentials. Apply this to TLS renewal, keyring changes and principal digests.
Policies already initialized in SQLite are updated through the authenticated
authorization API, rather than by replacing the bootstrap policy file alone.

## 4. Install and verify

Create a private values file containing names/settings, not credential bytes.
The following includes the image block required for a source checkout; omit
that block when installing a released OCI chart with its supplied image pin:

```yaml
fullnameOverride: moco
image:
  repository: registry.example.com/team/moco
  digest: sha256:REPLACE_WITH_THE_64_HEX_REGISTRY_DIGEST
credentials:
  existingSecret: moco-credentials
  revision: "1"
persistence:
  data:
    storageClass: YOUR_LOCAL_FILESYSTEM_STORAGE_CLASS
  backups:
    storageClass: YOUR_BACKUP_STORAGE_CLASS
backups:
  mirrorClaim: moco-offhost-mirror
  mirrorID: REPLACE_WITH_PROVISIONED_UUID
```

Replace all placeholders. Required image/Secret/mirror inputs intentionally
have no usable defaults. JSON Schema validation fails before deployment when
they are missing, malformed or unknown. Cross-field timing, distinct-claim and
monitoring guards also run in the templates. Helm's schema validation is
described in its [chart reference](https://helm.sh/docs/topics/charts/#schema-files).

```sh
helm lint --strict charts/moco -f /private/moco/values.yaml
helm template moco charts/moco -n moco -f /private/moco/values.yaml > /private/moco/rendered.yaml
# Review the rendered resources and storage configuration before installation.
helm upgrade --install moco charts/moco -n moco \
  -f /private/moco/values.yaml --wait --timeout 20m
kubectl -n moco rollout status statefulset/moco
kubectl -n moco logs moco-0 -c prepare-runtime
kubectl -n moco logs moco-0 -c server
kubectl -n moco logs moco-0 -c backup
```

The backup sidecar waits for local TLS readiness before its first job. Its
readiness requires a successful, fresh mirrored snapshot; the pod and Service
remain unready if backup validation fails or freshness expires. Server liveness
checks `/livez`; readiness checks `/readyz`, including SQLite, key epoch and the
audit failure latch. Native exec probes validate CA trust and localhost SAN.

For a local client, forward the public HTTPS port and use the CA:

```sh
kubectl -n moco port-forward statefulset/moco 8443:8443
# In another terminal, provide MOCO_TOKEN from administrator custody.
bin/moco --server https://localhost:8443 --ca-file /private/moco/ca.pem service info
bin/moco --server https://localhost:8443 --ca-file /private/moco/ca.pem tenants list
```

Also verify authorized writes, tenant isolation, rejected admin paths on the
public listener and fresh archives at both destinations. Do not infer actual
RPO/RTO or notification delivery solely from Kubernetes readiness.

## Storage sizing, upgrades and deletion

Data mounts at `/data`, with SQLite under `/data/moco/`. Local archives/status
live under `/backups/moco/`; the independently provisioned mirror uses
`/mirror/moco/`. `TMPDIR=/backups/moco` gives full-database verification disk
scratch rather than consuming the memory-backed `/tmp`. Allocate local backups
for **keep × archive size + approximately three full database sizes**, plus
growth. The default 100 Gi backup claim is an initial request, not sufficient
for 96 archives of a full 10 Gi database. Size both destinations for your actual
database, retention, growth and verification overhead.

For chart-managed claims, an empty `storageClass` uses the cluster default;
`-` emits `storageClassName: ""` for static binding. Verify the resulting PV,
its locality and access mode. Existing claims must be distinct, filesystem-backed
and solely dedicated to this release; a chart cannot verify the actual driver,
claim ownership, off-host identity or access modes of pre-existing resources.

StatefulSet claim names are `data-<fullname>-0` and `backups-<fullname>-0`.
Retention is `Retain` for both scaling and deletion. Uninstall removes workload
resources and keeps these claims; the external Secret/mirror/existing claims are
never owned by the release. Keep the release/fullname stable on reinstall.
Deleting a PVC separately may destroy its volume according to the PV reclaim
policy. There is no pre-delete hook that creates a final backup automatically.

Before upgrading, verify a fresh mirrored backup and separately recoverable
historical keys. RollingUpdate terminates the single old pod before replacing
it. Startup migrations run automatically; Helm rollback does not reverse a
database migration. Test releases against recovery copies before production.
Do not rely on `--atomic` to undo database changes.

Changing `volumeClaimTemplates` fields, storage classes, access modes or claim
selection can be rejected as an immutable StatefulSet change. For expansion,
use the CSI/PVC expansion procedure first and reconcile the StatefulSet template
through a reviewed maintenance plan; changing `size` in values is not a complete
resize procedure. Changing a claim name selects different data. Perform an
offline restore and verify it before reopening traffic.

## Recovery and key rotation

Use the matching release's `/moco-backup verify` and `restore` commands from
[the recovery runbook](https://github.com/araihu/moco/blob/main/docs/operations/backup-restore.md), with the mirror
archive and separately recovered keyring. Stop/fence the writer and backup
worker first; never overwrite a live database. For RWOP storage, stop the
StatefulSet pod before attaching the claim to an operator-controlled recovery
pod. Keep restored files private and owned by UID/GID 65532, and restore the
SQLite file to the dedicated `moco/moco.db` child path.

The chart intentionally supplies no destructive restore hook or restore-on-start
behavior. Recover into an offline claim, preserve old evidence, and reinstall
or start the one writer only after verification. Reconcile post-snapshot policy
revocations, discard cursors from the lost timeline, verify key epochs/ETags,
test cross-tenant denials and obtain a new verified mirrored backup.

For root-key rotation, retain historical eras in `keyring.json`, increase
`encryption.epoch` and `credentials.revision`, replace the external Secret and
upgrade. Restarting the pod recopies the keyring for both containers. Use the
private admin API for bounded rewrap sweeps and zero-old-key verification.
Retain old root keys in independent custody until every retained archive using
them expires. Measure recovery time and snapshot loss with real data sizes.

## Networking and monitoring

The public Service exposes HTTPS only. Administration defaults to loopback
9443; use an authorized `kubectl port-forward statefulset/moco 9443:9443` when
needed. The NetworkPolicy allows ingress to public 8443 and denies pod egress.
Set `networkPolicy.publicPeers` to restrict public clients/ingress controllers.
The node/kubelet/CSI performs filesystem storage I/O outside the pod's ordinary
TCP egress policy. Both main containers share a pod network; this does not
reproduce Compose's backup-only `network_mode: none` isolation.

Ingress is off by default. When enabled, set class, hostname, frontend TLS
Secret, and controller-specific annotations for **HTTPS upstream with CA and
hostname verification**. A Kubernetes Ingress object has no universal backend
TLS configuration. Review your controller's configuration and keep its trust
Secret separate from the server's root keyring. The chart routes only to the
public Service and never to administration.

To enable metrics, install Prometheus Operator CRDs first and create a separate
Secret containing **only** `metrics-token` and `ca.pem`. Then set:

```yaml
monitoring:
  enabled: true
  existingSecret: moco-metrics-only
  namespaceSelector:
    matchLabels:
      kubernetes.io/metadata.name: monitoring
  podSelector:
    matchLabels:
      app.kubernetes.io/name: prometheus
  labels:
    release: YOUR_PROMETHEUS_RELEASE
```

Both selectors must be nonempty. They are ANDed within one NetworkPolicy peer.
Enabling monitoring binds admin 9443 to the pod network, creates its private
ClusterIP Service and permits only the selected monitoring pods on that port.
NetworkPolicy requires an enforcing CNI and is L4: the same listener also hosts
administrative paths, which still require administrator authentication. The
metrics token does not grant admin privileges. The ServiceMonitor verifies TLS
using its CA Secret and reads its bearer credential through a Secret reference,
following the [Prometheus Operator API](https://prometheus-operator.dev/docs/api-reference/api/).

The private metrics Service publishes unready addresses, so failed public
readiness does not hide diagnostic metrics from authorized monitoring peers.

The ServiceMonitor uses job label `moco`; configure Prometheus to select its
labels/namespace. Import and adapt [the supplied alert rules](https://github.com/araihu/moco/blob/main/deploy/production/alerts.yaml),
including namespace/release scoping when running multiple installations. The
chart does not install Prometheus, Alertmanager, CRDs or a notification receiver.
Verify alerts for unready/audit failure, overdue/failed backups, disk pressure
and API errors, and test actual notification delivery. Protect and ship JSON
container logs through your cluster logging service. Monitor volume/mirror
availability, node clocks and certificate expiry independently.

## Chart development and validation

```sh
make chart-unittest                    # helm-unittest YAML suites only
make chart-check                       # Helm required; no cluster needed
make chart-check HELM=/path/to/helm
make check                             # application/CLI/operational checks
helm package charts/moco --destination /tmp
```

The five YAML suites in [`tests/`](tests/) use
[`helm-unittest`](https://github.com/helm-unittest/helm-unittest/tree/v1.2.1)
1.2.1. Their 40 cases cover container security, external credential handling,
SQLite writer isolation, retained/existing claims, verified backup configuration,
public HTTPS/Ingress, protected Prometheus scraping, and rejection of unsafe,
missing or unknown values. Use explicit assertions and named document selectors
when multiple resources come from one template; avoid order-dependent snapshots.

`make chart-unittest` downloads the upstream plugin archive on first use, verifies
its pinned SHA-256, caches it under `tools/bin/helm-unittest/`, and runs
`helm unittest --strict` with an isolated temporary plugin directory. It supports
Linux/macOS amd64/arm64, requires Helm, Bash, curl, tar and sha256sum (or macOS
shasum), and does not install global plugins. Cached archives are verified on
every run. JUnit output defaults to `bin/chart-tests.xml`; override it with
`CHART_TEST_REPORT=/path/to/report.xml`. Test suites and fixtures are excluded
from packaged charts through `.helmignore`.

`make chart-check` requires the YAML suites to pass, then runs the existing Go
integration tests against the selected real Helm executable: lint, packaging,
rendering, JSON Schema and template safety guards. This checks actual Helm 3/4
rendering as well as the plugin's embedded renderer. The separate CI job uses a
pinned Go container, pinned Helm setup action and both Helm versions above,
uploads JUnit reports even on failure, and gates release publication. It also
validates rendered built-in Kubernetes
resources with kubeconform 0.8.0 against Kubernetes 1.32 schemas. Application
operational tests exercise the real projected-Secret preparation path, TLS,
backups, rotation and recovery. These are not evidence of installation on your
specific Kubernetes distribution, CSI driver, ingress controller or CNI; run
the installation and recovery acceptance checks there before production.

## Values reference

See `values.yaml` for defaults and `values.schema.json` for the full Draft-07
contract. The following table includes all configurable leaf values.

| Value | Default | Description |
| --- | --- | --- |
| `image.repository` | `""` | Published image repository without a tag or digest. |
| `image.digest` | `""` | Required immutable OCI sha256 digest. |
| `image.pullPolicy` | `"IfNotPresent"` | Kubernetes image pull policy. |
| `imagePullSecrets` | `[]` | Existing registry credential Secrets. |
| `replicaCount` | `1` | Exactly one SQLite writer; scaling is unsupported. |
| `fullnameOverride` | `""` | Optional stable resource name (max 52 characters leaves room for suffixes). |
| `credentials.existingSecret` | `""` | Secret containing the seven documented file keys. |
| `credentials.revision` | `"1"` | Change this value when replacing credentials/certificates to recreate private copies. |
| `encryption.epoch` | `1` | Monotonically increase when changing the active root-key era. |
| `persistence.data.existingClaim` | `""` | Use a separately managed existing PVC; empty creates a claim. |
| `persistence.data.storageClass` | `""` | Local/block filesystem StorageClass. Empty uses the cluster default; - disables dynamic default selection. |
| `persistence.data.accessMode` | `"ReadWriteOncePod"` | Use ReadWriteOncePod with CSI. ReadWriteOnce is an explicit compatibility fallback. |
| `persistence.data.size` | `"10Gi"` | Requested capacity for a new claim; StatefulSet template changes require an expansion procedure. |
| `persistence.backups.existingClaim` | `""` | Use a separately managed existing PVC; empty creates a claim. |
| `persistence.backups.storageClass` | `""` | Local/block filesystem StorageClass. Empty uses the cluster default; - disables dynamic default selection. |
| `persistence.backups.accessMode` | `"ReadWriteOncePod"` | Use ReadWriteOncePod with CSI. ReadWriteOnce is an explicit compatibility fallback. |
| `persistence.backups.size` | `"100Gi"` | Requested capacity for a new claim; StatefulSet template changes require an expansion procedure. |
| `backups.mirrorClaim` | `""` | Pre-provisioned off-host filesystem PVC; never created by this chart. |
| `backups.mirrorID` | `""` | UUID already stored in mirror moco/.moco-destination; never bootstrapped automatically. |
| `backups.keep` | `96` | Managed archives retained per destination; pruning follows successful verification. |
| `backups.intervalSeconds` | `900` | Delay between backup starts; must exceed timeoutSeconds. |
| `backups.timeoutSeconds` | `600` | Per-job deadline; must be less than intervalSeconds. |
| `backups.maxAgeSeconds` | `1800` | Maximum verified snapshot age for readiness; must exceed interval + timeout. |
| `service.port` | `8443` | Public HTTPS Service port; container remains 8443. |
| `service.annotations` | `{}` | Service annotations. |
| `ingress.enabled` | `false` | Create a public Ingress. |
| `ingress.className` | `""` | IngressClass name; required when enabled. |
| `ingress.hostname` | `""` | Public certificate hostname; required when enabled. |
| `ingress.annotations` | `{}` | Controller annotations; must configure HTTPS upstream and CA verification. |
| `ingress.tlsSecret` | `""` | Existing frontend TLS Secret; required when enabled. |
| `networkPolicy.enabled` | `true` | Install NetworkPolicy. Required when monitoring enables the administrative listener. |
| `networkPolicy.publicPeers` | `[]` | Peers allowed to reach public port 8443. Empty allows every source. |
| `monitoring.enabled` | `false` | Enable metrics on the pod network (9443) and create ServiceMonitor. |
| `monitoring.existingSecret` | `""` | Separate metrics-token and ca.pem Secret, without root keys. |
| `monitoring.namespaceSelector` | `{}` | Standard Kubernetes scheduling/configuration value. |
| `monitoring.podSelector` | `{}` | Standard Kubernetes scheduling/configuration value. |
| `monitoring.labels` | `{}` | ServiceMonitor labels used by your Prometheus selector. |
| `monitoring.interval` | `"30s"` | Prometheus scrape interval. |
| `monitoring.serverName` | `"localhost"` | TLS name verified by Prometheus. |
| `resources.server.requests.cpu` | `"100m"` | Positive Kubernetes resource quantity |
| `resources.server.requests.memory` | `"256Mi"` | Positive Kubernetes resource quantity |
| `resources.server.limits.cpu` | `"2"` | Positive Kubernetes resource quantity |
| `resources.server.limits.memory` | `"1Gi"` | Positive Kubernetes resource quantity |
| `resources.backup.requests.cpu` | `"100m"` | Positive Kubernetes resource quantity |
| `resources.backup.requests.memory` | `"128Mi"` | Positive Kubernetes resource quantity |
| `resources.backup.limits.cpu` | `"1"` | Positive Kubernetes resource quantity |
| `resources.backup.limits.memory` | `"512Mi"` | Positive Kubernetes resource quantity |
| `resources.init.requests.cpu` | `"100m"` | Positive Kubernetes resource quantity |
| `resources.init.requests.memory` | `"64Mi"` | Positive Kubernetes resource quantity |
| `resources.init.limits.cpu` | `"1"` | Positive Kubernetes resource quantity |
| `resources.init.limits.memory` | `"256Mi"` | Positive Kubernetes resource quantity |
| `runtime.serverMemoryLimit` | `"768MiB"` | Server GOMEMLIMIT below its container memory limit. |
| `runtime.backupMemoryLimit` | `"384MiB"` | Backup GOMEMLIMIT below its container memory limit. |
| `runtime.serverMaxProcs` | `2` | Server GOMAXPROCS. |
| `runtime.backupMaxProcs` | `1` | Backup GOMAXPROCS. |
| `runtime.tmpSizeLimit` | `"512Mi"` | Memory-backed /tmp capacity. |
| `runtime.credentialsSizeLimit` | `"16Mi"` | Memory-backed private credential capacity. |
| `nodeSelector` | `{}` | Node labels. Choose nodes supporting the local volume topology. |
| `tolerations` | `[]` | Standard Kubernetes pod tolerations. |
| `affinity` | `{}` | Standard Kubernetes affinity; the storage class/PV must enforce data locality. |
| `podAnnotations` | `{}` | Additional pod annotations; credential revision is chart-managed. |

## Declarative management controller

`controllerAccess.enabled` opens a dedicated private ClusterIP administrative Service and requires explicit nonempty namespace and pod selectors plus NetworkPolicy. It is independent of `monitoring.enabled`. Include the controller in public API peers and issue a certificate covering both configured DNS names. See the [controller runbook](../../docs/operations/kubernetes-controller.md) for credentials and `MocoTenant`, `MocoVault`, `MocoAuthorization` examples.
