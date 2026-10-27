# Install Moco 0.0.3

Choose a **single-instance SQLite** deployment. Both images support Linux amd64
and arm64. The server stores data; the optional controller manages metadata and
authorization. ESO delivers values to workloads.

## Docker Compose

Follow the [production SQLite runbook](operations/production-sqlite.md) to provision
private directories, TLS certificates, an authorization file, encryption keyring
and an independently mounted backup mirror. The repository includes
[Compose configuration](../deploy/production/compose.yaml), metrics and alerts.
Use the released image `ghcr.io/araihu/moco:0.0.3`, preferably recorded by digest.
Do not use the localhost development token mode for this profile.

## Kubernetes

Before installing, review the [server chart](../charts/moco/README.md): it documents
required private credentials, TLS SANs, storage ownership, mirror sentinel,
NetworkPolicy and values schema. The following commands assume those inputs
already exist; they are not a credential bootstrap procedure.

```sh
helm show values oci://ghcr.io/araihu/charts/moco --version 0.0.3
helm upgrade --install moco oci://ghcr.io/araihu/charts/moco \
  --version 0.0.3 --namespace moco --create-namespace \
  -f /private/moco/server-values.yaml --wait --timeout 5m
kubectl get pods --namespace moco -l app.kubernetes.io/instance=moco
```

The released chart pins the server image digest. Keep one writer, retain the
PVC on upgrades and take a verified backup before changing the server version.
The mirror must provide an independent failure domain for disaster recovery.

## Optional controller and ESO

Bootstrap a dedicated management principal and a Secret containing only its token
and CA. Follow the [controller runbook](operations/kubernetes-controller.md) before
installing the separate chart:

```sh
helm upgrade --install moco-management oci://ghcr.io/araihu/charts/moco-controller \
  --version 0.0.3 --namespace moco \
  -f /private/moco/controller-values.yaml --wait --timeout 5m
```

Declare `MocoTenant`, `MocoVault` and singleton `MocoAuthorization/moco` in that
trusted namespace. Authorization replaces the entire snapshot and requires
explicit initial revision adoption. Deleting CRs retains remote resources.
Helm does not upgrade CRDs automatically; review and apply CRD updates separately.

ESO uses its generic webhook provider with a distinct, scoped reader principal.
It is not a native Moco provider. See [the integration examples and acceptance
suite](operations/kubernetes-eso-tests.md) for CA trust, Base64 decoding and refresh.

## Validate before keeping secrets

Check readiness, protected metrics, authorization denials, automatic backups,
mirror failure alerts and a restore with the retained historical keys.
[The 0.0.3 test record](releases/0.0.3.md) describes what has been exercised;
repeat recovery and storage checks on your own infrastructure.
