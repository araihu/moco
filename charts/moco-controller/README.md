# Controller Helm chart

Install the optional controller to manage tenants, vaults and permissions through
Kubernetes resources. It connects to an existing Moco server and does not store
secret values. Follow the [controller guide](../../docs/operations/kubernetes-controller.md)
for credential bootstrap, NetworkPolicy and resource examples.

## Install

Create `moco-controller-credentials` in namespace `moco` with only `token` and
`ca.pem`. The token must belong to the dedicated management principal configured
on the server. Keep credential bytes out of Helm values.

Save your endpoints in `/private/moco/controller-values.yaml`:

```yaml
publicURL: https://moco.moco.svc:8443
adminURL: https://moco-admin.moco.svc:9443
```

The TLS certificate must cover both names. Install the released chart:

```sh
helm upgrade --install moco-management oci://ghcr.io/araihu/charts/moco-controller \
  --version 0.0.3 --namespace moco \
  -f /private/moco/controller-values.yaml --wait --timeout 5m
```

## Settings

| Value | Default | Use |
| --- | --- | --- |
| `publicURL` | required | Moco public HTTPS endpoint |
| `adminURL` | required | Private admin HTTPS endpoint |
| `credentialsSecret` | `moco-controller-credentials` | Existing Secret in this namespace |
| `subject` | `moco-controller` | Principal ID matching the token |
| `reconcileInterval` | `30s` | Interval for reconciling drift |
| `imagePullSecrets` | `[]` | Registry credentials when required |
| `resources` | 50m CPU / 128Mi request, 512Mi memory limit | Controller resources |

The released chart supplies its image repository and digest. Values are
validated by JSON Schema. Install one controller release per Moco instance and
namespace; Kubernetes leader election does not coordinate separate clusters.

Helm installs the three CRDs on first install, but does not upgrade them.
[Review and apply new CRDs before upgrading](../../docs/operations/kubernetes-controller.md#authoritative-authorization-and-gitops).
Deleting a CR retains its corresponding data in Moco. Uninstalling the chart
retains CRDs and does not delete remote tenants, vaults or permissions.
