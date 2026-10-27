# Manage resources with Kubernetes

Use the Moco controller to keep tenants, vaults and permissions in Git. Install
one controller release for one Moco instance in a trusted namespace. Anyone
allowed to write its CRs can administer that instance; restrict Kubernetes RBAC.
Do not connect controllers from separate namespaces or clusters to the same server.

| Resource | What you declare |
| --- | --- |
| `MocoTenant` | Tenant name, description, labels and a stable external ID |
| `MocoVault` | Vault metadata and a tenant reference in the same namespace |
| `MocoAuthorization/moco` | The complete policy and role-binding snapshot |

Deleting a CR retains the remote resource and its secrets. Delete data explicitly
through Moco when needed. Keep external IDs stable: they allow existing resources
to be adopted after status loss. Tenant references and external IDs cannot be
changed in place.

The controller checks for drift every 30 seconds by default and restores the
declared state. Wait for `Ready=True` before using a tenant or vault's ID.
Failed operations set `Ready=False`; inspect the condition before changing state.

## Install and bootstrap

Use the published controller OCI image and chart from release v0.0.3. This chart is separate from the single-writer server chart; it does not install SQLite, store encryption keys, or manage secret values. Use the released chart with its image digest already pinned:

```sh
helm upgrade --install moco-management oci://ghcr.io/araihu/charts/moco-controller -n moco \
  --version 0.0.3 -f /private/moco/controller-values.yaml
```

For private controller images, create a read-only registry Secret in this namespace and set `imagePullSecrets: [{name: ghcr-readonly}]`. This is distinct from the Moco management credential Secret. Authenticate Helm to GHCR when the chart package is private.

Before installing:

1. Provision a dedicated random management token outside Git. Add its SHA-256 digest as principal `moco-controller` to Moco's startup `access.json`. Set the chart's `subject` to the same principal ID. Principal identities/token digests remain startup configuration, not authorization CR data.
2. Bootstrap that principal's management permissions in the authoritative authorization snapshot using the existing admin API. The exact ten management permissions are listed below; changing `access.json` policies alone does not replace an already initialized database snapshot.
3. Create a Secret named `moco-controller-credentials` in the controller namespace with **only** `token` and `ca.pem`. Do not mount the server credential Secret or root encryption keyring into the controller. Secret reads bypass the informer cache and RBAC grants only `get` on that named Secret. Provision it through your existing secure secret bootstrap process.
4. Enable the server chart's `controllerAccess` using explicit namespace **and** pod selectors. Add the controller to `networkPolicy.publicPeers` as well. Ensure its TLS certificate covers both configured service DNS names. For the names above, include `moco.moco.svc` and `moco-admin.moco.svc` in the SANs. Administrative access remains ClusterIP-only and independent of Prometheus.

Example server chart values:

```yaml
controllerAccess:
  enabled: true
  namespaceSelector:
    matchLabels: {kubernetes.io/metadata.name: moco}
  podSelector:
    matchLabels: {app.kubernetes.io/name: moco-controller, app.kubernetes.io/instance: moco-management}
networkPolicy:
  publicPeers:
    - namespaceSelector:
        matchLabels: {kubernetes.io/metadata.name: moco}
      podSelector:
        matchLabels: {app.kubernetes.io/name: moco-controller, app.kubernetes.io/instance: moco-management}
    # Include separate ESO/application peers as required.
```

The reserved management principal always receives these policies with domain `*`:

| Path | Methods |
| --- | --- |
| `/api/v1/tenants` | GET, POST |
| `/api/v1/tenants/{tenantId}` | GET, PUT |
| `/api/v1/tenants/{tenantId}/vaults` | GET, POST |
| `/api/v1/tenants/{tenantId}/vaults/{vaultId}` | GET, PUT |
| `/internal/v1/authorization` | GET, PUT |

These permissions do not grant secret reads/writes, deletion, audit access or encryption operations. The controller always injects them into the desired snapshot to preserve reconciliation. Preserve any additional break-glass administrator permissions explicitly in the CR. You can copy existing administrator policies from GET `/internal/v1/authorization`; no token digests appear in that response.

## Create a tenant and vault

Save the following as `resources.yaml` and keep their external IDs stable:

```yaml
apiVersion: moco.araihu.io/v1alpha1
kind: MocoTenant
metadata:
  name: homelab
spec:
  externalId: kubernetes://homelab/platform/tenant-homelab
  name: homelab
---
apiVersion: moco.araihu.io/v1alpha1
kind: MocoVault
metadata:
  name: applications
spec:
  tenantRef: homelab
  externalId: kubernetes://homelab/platform/vault-applications
  name: applications
```

```sh
kubectl -n moco apply -f resources.yaml
kubectl -n moco wait --for=condition=Ready mocotenant/homelab --timeout=2m
kubectl -n moco wait --for=condition=Ready mocovault/applications --timeout=2m
kubectl -n moco get mocotenant homelab -o jsonpath='{.status.id}'
kubectl -n moco get mocovault applications -o jsonpath='{.status.id}'
```

Use those IDs with the CLI or ESO. Updating metadata in Git changes it in Moco;
changes made directly to managed metadata are reverted on reconciliation.

## Authoritative authorization and GitOps

Apply [the example resources](../../deploy/examples/controller.yaml) after replacing example bootstrap policy subjects as necessary. Policies and role bindings select exactly one of `domain` (literal UUID or explicit `*`) and `tenantRef` (same-namespace CR name). A policy's `path` remains a literal Moco route/pattern; use the normal canonical secret route plus `secretPathPrefix` for scoped secret paths, or a literal vault URL when limiting to one vault.

Read GET `/internal/v1/authorization` with your existing administrator and set `spec.adoptRevision` to its current `revision` before first applying the snapshot. This explicitly approves **replacing every existing policy and role binding**. A missing tenant reference blocks the whole replacement. A second authorization CR name is rejected by the CRD schema. Changing the snapshot out of band is treated as drift after successful adoption.

If Kubernetes status is lost and the remote snapshot already matches, status is recovered without a write. If status is lost and the remote snapshot differs, update `adoptRevision` to its current revision only after reviewing the complete replacement. External mutations use If-Match; HTTP 409/412 produce Ready=False and a fresh read on the next reconciliation.

CRDs are installed from the chart's `crds/` directory. Helm does not upgrade or uninstall CRDs automatically. For controller API updates, review and apply the new CRDs first:

```sh
helm pull oci://ghcr.io/araihu/charts/moco-controller --version NEXT_VERSION \
  --untar --untardir /private/moco/upgrade
kubectl apply --server-side -f /private/moco/upgrade/moco-controller/crds/
helm upgrade moco-management oci://ghcr.io/araihu/charts/moco-controller \
  --version NEXT_VERSION -n moco -f /private/moco/controller-values.yaml
kubectl get mocotenants,mocovaults,mocoauthorizations -n moco
```

A CR Ready condition means its desired remote state was observed/applied; it does not replace server backup/readiness monitoring. HTTP `/healthz` and `/readyz` on the controller's pod port 8081 cover process health, and the deployment uses non-root, a read-only filesystem and namespace-scoped RBAC. Endpoint/namespace changes require an explicitly reviewed migration; do not repoint a live controller with old CR status at another instance.

## Deliver values with ESO

The controller does not create application Secrets or principal tokens. Use the
Ready tenant/vault IDs in [ESO configuration](eso.md). Apply dependent resources
after they become Ready; the controller does not rewrite ESO manifests.

For a failing CR, inspect its conditions without printing credentials:

```sh
kubectl -n moco describe mocotenant homelab
kubectl -n moco describe mocovault applications
kubectl -n moco get mocoauthorization moco -o yaml
kubectl -n moco logs deployment/moco-management-controller
```

Use the deployment name shown by `kubectl -n moco get deployments` if you chose
a different release/fullname. For missing references, wait for the tenant's Ready
condition. For `401`/`403`, check the management principal and token. For `409`/`412`,
review current remote state and authorization adoption before retrying.
