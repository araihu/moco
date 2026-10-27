# Kubernetes acceptance with vind and External Secrets Operator

Run a disposable real Kubernetes cluster with vind (vCluster's Docker driver),
Calico, the published Moco server/controller charts, and External Secrets Operator's webhook
provider. The test downloads checksum-pinned tools and manifests, creates fresh
credentials and a private CA, and never uses production credentials.

## Run

Requirements: Linux/macOS amd64/arm64, a working local Docker daemon, Python 3,
OpenSSL 3, Helm 3/4, and enough Docker memory
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

The default mode tests published **v0.0.3** artifacts. It verifies the immutable
`release.json` checksum, pulls both charts from GHCR by version, checks their
OCI digests and archive SHA-256 values, and validates the default image pins and
controller CRDs before creating the cluster. Both charts are installed from
those downloaded archives. Bootstrap uses `/moco-ops` from the released server
image; no application binary or image is built from the checkout. The chart image
defaults remain in use, and the suite verifies installed chart/app versions and
running Pod images against the release pins.

For controller development, `make vind-source-test` builds the bootstrap
operations and controller image from the checkout, imports the image into the
vind node, and uses the source charts. This optional mode requires Go and Docker
buildx. Select **source** in the workflow input, or use `--source` with the wrapper.
The report explicitly distinguishes `published-release` from `source`.

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
gh workflow run vind-eso.yml --repo araihu/moco -f mode=release
```

## Scenarios

The acceptance report records each scenario independently:

1. Install a real vind cluster, enforcing Calico CNI, ServiceMonitor CRD and ESO.
2. Provision distinct data, local-backup, mirror and restore volumes; initialize
   the mirror marker with the real `moco-ops` image.
3. Bootstrap credentials with `moco-ops init`, deploy the production chart, and
   wait for TLS and verified mirrored-backup readiness.
4. Install the published controller chart and establish the three CRDs with
   namespace-scoped RBAC and private TLS access.
5. Verify both installed chart/app versions and their running OCI image digests.
6. Create `MocoTenant`, `MocoVault` and `MocoAuthorization` declaratively; wait for
   tenant references, verify Ready conditions, reconcile the full authorization
   snapshot, and repeat apply without creating new resources.
7. Repair tenant/authorization drift, recover status after controller restart,
   validate immutable CRD identities and singleton authorization, and refresh
   repaired controller credentials.
8. Deny other tenants/vaults, reader writes, unauthenticated reads and public
   administrative API requests.
9. Apply `SecretStore`/`ExternalSecret` twice, fetch a URL-escaped logical path,
   decode a binary secret exactly once, and mount it in a consumer Pod.
10. Update the secret conditionally, reject a stale ETag, and observe both the
    Kubernetes Secret and the running consumer's volume update.
11. Reject an invalid ESO bearer credential, retain the last valid Secret, and
    recover after repairing the credential.
12. Reject an invalid CA and recover without disabling TLS verification.
13. Renew the server certificate and verify the certificate actually served.
14. Rotate reader and controller tokens and prove old tokens are rejected.
15. Prove public/admin NetworkPolicy enforcement and protected TLS metrics.
16. Observe automatic backups and compare local/mirror archive bytes.
17. Rotate root keys, retain historical keys, advance the epoch and rewrap keys.
18. Remove the mirror marker, observe failed backups/readiness, retain the
    consumer Secret, then restore the marker and recover.
19. Replace the Moco Pod and prove SQLite data persists.
20. Stop the writer, restore a pre-rotation mirrored snapshot into a different
    offline volume, restart and prove ESO recreates the application Secret.
21. Delete the management CRs and verify retained Moco resources and ESO access.

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
component pins, installed artifact identities, declarative resource IDs/conditions and limitations. CI uploads this report and sanitized Pod status
diagnostics for seven days. It never uploads kubeconfigs, credential files,
Secret objects, secret responses or test database archives.

These tests use static local **ReadWriteOnce** volumes on one disposable node.
They do not certify a production CSI driver, ReadWriteOncePod fencing,
independent off-host durability, actual homelab RPO/RTO, ingress-controller
behavior or external alert delivery. The mirror is a separate test volume on
the same Docker host. Validate those properties on the real homelab as well.

To keep a published-release test cluster for debugging, run:

```sh
python3 -B scripts/vind_test.py --keep
```

The printed private workspace holds the matching CLI config and kubeconfig.
Use that config with `vcluster --config PATH/config.json delete CLUSTER --driver
docker` afterward, then remove the private workspace. Treat a kept cluster and
its workspace as sensitive even though its credentials are disposable.

## Management controller acceptance

The default acceptance path uses the published controller image and chart. Tenant,
vault and authorization setup goes through the CRDs before configuring ESO; only
secret values and out-of-band drift/failure fixtures use direct API writes.
`artifactMode`, `releaseCommit`, `installedArtifacts` and `declarativeResources`
in the report establish which binaries/charts and reconciliation outcomes were
tested. The optional source mode builds a temporary OCI archive and imports its
digest into the node. See [the controller runbook](kubernetes-controller.md).
