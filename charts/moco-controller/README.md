# Moco management controller chart

Installs the three structural, namespaced CRDs and one namespace-scoped controller deployment. Read the [installation and GitOps runbook](../../docs/operations/kubernetes-controller.md) for bootstrap, full-snapshot authorization adoption, RBAC, TLS/network access, rotation and CRD upgrades. All values are validated against `values.schema.json`.

Required values: `image.repository`, `image.digest`, `publicURL`, `adminURL`. Optional values: `credentialsSecret` (default `moco-controller-credentials`), `subject` (`moco-controller`), `reconcileInterval` (`30s`), `resources` (CPU/memory requests and memory limit). The Secret must contain `token` and `ca.pem` in this release namespace. Never store token values in Helm values. The chart creates no Secrets and no ClusterRoles. Install one release per namespace and Moco instance.

Helm installs CRDs on first install; apply reviewed CRDs separately before upgrades. Helm uninstall retains CRDs, and deletion of Moco CRs retains server resources and data. The default chart has no published image reference: build `controller/Dockerfile`, publish and supply an immutable digest.

Run `make chart-check` from the repository root to execute helm-unittest, schema, rendering and package checks. `make vind-test` validates this chart in real Kubernetes with ESO.
