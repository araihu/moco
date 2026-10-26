# Kubernetes acceptance with vind and External Secrets Operator

Run a disposable real Kubernetes cluster with vind (vCluster's Docker driver),
Calico, the Moco production chart, and External Secrets Operator's webhook
provider. The test downloads checksum-pinned tools and manifests, creates fresh
credentials and a private CA, and never uses production credentials.

## Run

Requirements: Linux/macOS amd64/arm64, a working local Docker daemon, Python 3,
OpenSSL 3, Helm 3/4, Go matching the repository floor, and enough Docker memory
for Kubernetes, Calico and the test workloads (8 GiB recommended).

```sh
make vind-test
```

The runner creates an independently named cluster, uses a temporary vCluster
configuration and kubeconfig, and deletes the cluster at the end, including
after failure. It does not switch the user's global Kubernetes context or
vCluster driver. Tool downloads are cached under ignored `tools/bin/kubernetes/`;
checksums and component versions are in
[`deploy/testing/versions.json`](../../deploy/testing/versions.json).

The Moco server image is the pinned **0.0.2 release**; the chart and bootstrap
operations are taken from the source checkout. This is a compatibility test
against that release, not a build of the current server source. Update the pin
deliberately when changing the tested release.

An SSH Docker context cannot bind the runner's local temporary files on the
daemon machine. Run the command on the Docker host in that case. A nested
LXC Docker host must provide the kernel facilities needed by kubelet and the
CNI. The development LXC/OrbStack environment exposes `/proc/kmsg` but denies
access to it, preventing kubelet from starting. This is a cluster bootstrap
failure, not a successful application test. Do not substitute fake kernel
devices or disable the network policies to obtain a passing report.

The `vind-eso` GitHub Actions workflow runs the same suite on an isolated
Ubuntu Docker runner. Dispatch it from the Actions tab or:

```sh
gh workflow run vind-eso.yml --repo araihu/moco
```

## Scenarios

The acceptance report records each scenario independently:

1. Install a real vind cluster, enforcing Calico CNI, ServiceMonitor CRD and ESO.
2. Provision distinct data, local-backup, mirror and restore volumes; initialize
   the mirror marker with the real `moco-ops` image.
3. Bootstrap credentials with `moco-ops init`, deploy the production chart, and
   wait for TLS and verified mirrored-backup readiness.
4. Replay idempotent tenant creation, create a dedicated vault and apply a
   revision-guarded authorization snapshot through the private admin listener.
5. Deny other tenants/vaults, reader writes, unauthenticated reads and public
   administrative API requests.
6. Apply `SecretStore`/`ExternalSecret` twice, fetch a URL-escaped logical path,
   decode a binary secret exactly once, and mount it in a consumer Pod.
7. Update the secret conditionally, reject a stale ETag, and observe both the
   Kubernetes Secret and the running consumer's volume update.
8. Reject an invalid ESO bearer credential, retain the last valid Secret, and
   recover after repairing the credential.
9. Reject an invalid CA and recover without disabling TLS verification.
10. Renew the server certificate, update the external Secret/revision, and
    verify the certificate actually served after the private-copy rollout.
11. Rotate the reader token and prove the old token is rejected.
12. Prove public and admin NetworkPolicy enforcement with allowed/denied Pods;
    fetch authenticated TLS metrics and inspect the ServiceMonitor reference.
13. Observe repeated automatic backups and compare local/mirror archive bytes.
14. Rotate root keys, retain the historical key, advance the epoch, and rewrap
    all live vault keys through the administrative API.
15. Remove the mirror marker, observe failed backups and loss of Pod readiness,
    preserve the last consumer Secret, then restore the marker and recover.
16. Replace the Moco Pod and prove SQLite data persists.
17. Stop the writer, restore a pre-rotation mirrored snapshot into a different
    offline volume with historical keys, start the single writer on it, delete
    the consumer's Kubernetes Secret and prove ESO recreates it from Moco.

The declarative webhook resources are in
[`deploy/testing/eso.yaml`](../../deploy/testing/eso.yaml). They reference a
Secret containing only the scoped reader token and public CA, use `$.value`
and `decodingStrategy: Base64`, and retain the application Secret when the
upstream service fails. Never grant ESO the bootstrap administrator token.
ESO already URL-encodes `remoteRef.key` when expanding the URL: use
`{{ .remoteRef.key }}` directly and do not add `urlquery`. Otherwise slashes,
plus signs and spaces are encoded twice and Moco looks up a different path.
See the upstream [ESO webhook documentation](https://external-secrets.io/latest/provider/webhook/)
and [the pinned implementation](https://github.com/external-secrets/external-secrets/blob/helm-chart-2.14.0/providers/v1/webhook/pkg/webhook/webhook.go).

## Evidence and limits

`bin/kubernetes-report.json` records successful/failed scenarios, durations,
component pins and limitations. CI uploads this report and sanitized Pod status
diagnostics for seven days. It never uploads kubeconfigs, credential files,
Secret objects, secret responses or test database archives.

These tests use static local **ReadWriteOnce** volumes on one disposable node.
They do not certify a production CSI driver, ReadWriteOncePod fencing,
independent off-host durability, actual homelab RPO/RTO, ingress-controller
behavior or external alert delivery. The mirror is a separate test volume on
the same Docker host. Validate those properties on the real homelab as well.

To keep a test cluster for debugging, first build `bin/moco-ops`, then run:

```sh
python3 -B scripts/vind_test.py --keep
```

The printed private workspace holds the matching CLI config and kubeconfig.
Use that config with `vcluster --config PATH/config.json delete CLUSTER --driver
docker` afterward, then remove the private workspace. Treat a kept cluster and
its workspace as sensitive even though its credentials are disposable.
