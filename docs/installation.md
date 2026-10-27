# Install Moco

Moco 0.0.3 runs as one server with local SQLite storage. Choose Docker Compose
for a Linux host or Helm for Kubernetes. Add the controller only if you want
to manage tenants, vaults and permissions through Kubernetes resources.

## Docker Compose

Follow the [single-instance runbook](operations/production-sqlite.md) to create
private storage, TLS certificates, credentials and an off-host backup mirror.
It includes the configuration and startup commands for the released image
`ghcr.io/araihu/moco:0.0.3`.

Do not use the disposable walkthrough's shared token for a persistent deployment.
Keep encryption keys in separate recovery custody before storing real secrets.

## Kubernetes

Follow the [server Helm guide](../charts/moco/README.md) to provision credentials,
local data storage and the backup mirror. Then install the released chart with
your private values file:

```sh
helm show values oci://ghcr.io/araihu/charts/moco --version 0.0.3
helm upgrade --install moco oci://ghcr.io/araihu/charts/moco \
  --version 0.0.3 --namespace moco --create-namespace \
  -f /private/moco/server-values.yaml --wait --timeout 20m
kubectl get pods --namespace moco -l app.kubernetes.io/instance=moco
```

The chart pins the server image. Keep one writer and retain its data PVC on
upgrades. SQLite data must not use NFS/CIFS; the backup mirror must survive loss
of the server's host.

## Optional controller and ESO

[Configure the controller](operations/kubernetes-controller.md) with a dedicated
management token before installing its separate chart. It manages resource
metadata and permissions, not secret values.

[Connect ESO](operations/eso.md) with a separate reader token to deliver values
into Kubernetes Secrets. ESO uses its webhook provider; it does not need the
Moco controller to be installed.

## Before storing real secrets

Verify server readiness, an authorized read/write, a denied request from a
restricted client, a fresh mirrored backup and an offline restore with the keys
held outside the deployment. Configure [monitoring and alerts](operations/production-sqlite.md#observability-and-response).
