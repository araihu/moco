# Declarative Moco management controller

The independent Go module in `controller/` manages one Moco instance from one Kubernetes namespace. Install exactly one controller release per Moco instance and namespace. Do not point controllers in different namespaces or clusters at the same instance. Leader election serializes replicas in the managed namespace; it cannot coordinate separate clusters. Anyone allowed to write these CRs effectively administers that Moco instance: keep them in a trusted platform namespace and restrict RBAC accordingly.

| CRD | Desired state | Status |
| --- | --- | --- |
| `MocoTenant` | tenant name, description, labels, immutable `externalId` | ID, revision, observed generation, Ready condition |
| `MocoVault` | vault metadata and immutable same-namespace `tenantRef` | vault ID, tenant ID, revision, Ready condition |
| `MocoAuthorization` | the entire policy/role-binding snapshot, named **moco** | snapshot revision, observed generation, Ready condition |

Deleting a CR **retains** the corresponding remote resource, secrets and authorization. There is no cascade deletion or delete permission in the controller. Remove retained data explicitly through the Moco API when appropriate. A tenant or vault with a matching explicit external ID is adopted, including after Kubernetes status loss; names and status IDs alone never authorize adoption. Duplicate external IDs among managed CRs are rejected. The tenant reference and external ID cannot change in place. Recreating a tenant with a different server ID does not move an existing vault.

The controller repairs external metadata and authorization drift every `reconcileInterval` (default 30 seconds), waits for tenant references to become Ready at their current generation, and performs replacements with fresh ETag/If-Match preconditions. Failed operations expose a Ready=False condition and retry after the interval; uncertain mutations are never blindly replayed. HTTPS certificate verification, explicit CA trust, bounded HTTP requests and rejected redirects protect the management credential. Error conditions omit API response bodies and secret material.

## Install and bootstrap

Build and publish the controller OCI image from the repository checkout, then supply its registry digest. This chart is separate from the single-writer server chart; it does not install SQLite, store encryption keys, or manage secret values. Release v0.0.3 and later publish the controller image and chart alongside the server. Use the released chart with its image digest already pinned:

```sh
helm upgrade --install moco-management oci://ghcr.io/araihu/charts/moco-controller -n moco \
  --version 0.0.3 -f /private/moco/controller-values.yaml
```

For source builds and development, build and distribute the controller image and use the local chart:

```sh
docker build -t REGISTRY/moco-controller:VERSION controller
docker push REGISTRY/moco-controller:VERSION
helm upgrade --install moco-management charts/moco-controller -n moco \
  --set image.repository=REGISTRY/moco-controller \
  --set image.digest=sha256:REPLACE_WITH_PUBLISHED_DIGEST \
  --set publicURL=https://moco.moco.svc:8443 \
  --set adminURL=https://moco-admin.moco.svc:9443
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

## Authoritative authorization and GitOps

Apply [the example resources](../../deploy/examples/controller.yaml) after replacing example bootstrap policy subjects as necessary. Policies and role bindings select exactly one of `domain` (literal UUID or explicit `*`) and `tenantRef` (same-namespace CR name). A policy's `path` remains a literal Moco route/pattern; use the normal canonical secret route plus `secretPathPrefix` for scoped secret paths, or a literal vault URL when limiting to one vault.

Read GET `/internal/v1/authorization` with your existing administrator and set `spec.adoptRevision` to its current `revision` before first applying the snapshot. This explicitly approves **replacing every existing policy and role binding**. A missing tenant reference blocks the whole replacement. A second authorization CR name is rejected by the CRD schema. Changing the snapshot out of band is treated as drift after successful adoption.

If Kubernetes status is lost and the remote snapshot already matches, status is recovered without a write. If status is lost and the remote snapshot differs, update `adoptRevision` to its current revision only after reviewing the complete replacement. External mutations use If-Match; HTTP 409/412 produce Ready=False and a fresh read on the next reconciliation.

CRDs are installed from the chart's `crds/` directory. Helm does not upgrade or uninstall CRDs automatically. For controller API updates, review and apply the new CRDs first:

```sh
kubectl apply --server-side -f charts/moco-controller/crds/
helm upgrade moco-management charts/moco-controller -n moco -f controller-values.yaml
kubectl get mocotenants,mocovaults,mocoauthorizations -n moco
```

A CR Ready condition means its desired remote state was observed/applied; it does not replace server backup/readiness monitoring. HTTP `/healthz` and `/readyz` on the controller's pod port 8081 cover process health, and the deployment uses non-root, a read-only filesystem and namespace-scoped RBAC. Endpoint/namespace changes require an explicitly reviewed migration; do not repoint a live controller with old CR status at another instance.

## ESO and operational verification

ESO continues to deliver secret values using its generic webhook provider. This controller does not create Kubernetes application Secrets or principal tokens. Use the Ready tenant/vault IDs in `SecretStore` configuration as shown in [the ESO runbook](kubernetes-eso-tests.md). Both resources can be rendered by GitOps after their dependencies become Ready; this initial version does not rewrite ESO manifests automatically.

`make controller-check` runs generation, race-tested reconciliation/HTTP tests and vet; `make check` also includes controller linting and vulnerability scanning. `make chart-check` runs helm-unittest for both charts plus real Helm render/schema/package tests. `make vind-test` builds the current controller into an OCI archive, imports its digest into a disposable vind node, installs the charts and ESO, and exercises real CRD admission, missing-reference recovery, idempotency, drift repair, status recovery across restart, Secret credential refresh, retained deletion and ESO delivery alongside the backup/restore/failure drills. The `vind-eso` workflow runs the same harness on a native Docker host when the local daemon is unsuitable.

The image is built from the checked out controller source, while the unchanged server acceptance baseline remains the pinned released Moco image. The test creates/deletes only its unique cluster and temporary buildx builder. Reports contain no secret payloads. See the ESO runbook for storage and single-node test limitations.
