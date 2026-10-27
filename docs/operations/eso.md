# Deliver secrets with External Secrets Operator

ESO reads a value from Moco and writes it into a Kubernetes Secret for your
workload. Use ESO's webhook provider and a dedicated reader token. Moco's
controller is optional; it manages metadata and permissions, not delivery.

## Before you start

- Install [External Secrets Operator](https://external-secrets.io/latest/introduction/getting-started/).
- Create a tenant, vault and secret in Moco. Record their IDs and the secret path.
- Give the reader principal `GET` access to that vault's secret route and the
  required `secretPathPrefix`, such as `apps/`. See [access configuration](../security.md#grant-access).
- Allow the ESO controller pods through Moco's public NetworkPolicy. Its TLS
  certificate must cover the hostname used below; do not expose the admin listener.

## Supply the reader credential

Create the credential Secret in the application's namespace. The files contain
only the reader token and Moco's public CA certificate:

```sh
kubectl -n applications create secret generic moco-reader \
  --from-file=token=/private/moco/eso-reader-token \
  --from-file=ca.pem=/private/moco/ca.pem
```

Never put the administrator token or encryption keyring in this Secret.
The namespace must already exist, and ESO must be permitted to read this Secret.

## Create a SecretStore and ExternalSecret

Save the following as `moco-secrets.yaml`. Replace `TENANT_ID` and `VAULT_ID`,
use your Moco HTTPS hostname, and set `remoteRef.key` to an existing secret path:

```yaml
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata:
  name: moco
  namespace: applications
spec:
  provider:
    webhook:
      url: 'https://moco.moco.svc:8443/api/v1/tenants/TENANT_ID/vaults/VAULT_ID/secret?path={{ .remoteRef.key }}'
      method: GET
      timeout: 5s
      headers:
        Authorization: 'Bearer {{ .reader.token }}'
      caProvider:
        type: Secret
        name: moco-reader
        key: ca.pem
      secrets:
        - name: reader
          secretRef:
            name: moco-reader
      result:
        jsonPath: $.value
---
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata:
  name: database
  namespace: applications
spec:
  refreshInterval: 1m
  secretStoreRef:
    name: moco
    kind: SecretStore
  target:
    name: database
    creationPolicy: Owner
    deletionPolicy: Retain
  data:
    - secretKey: password
      remoteRef:
        key: apps/database/password
        decodingStrategy: Base64
```

```sh
kubectl apply -f moco-secrets.yaml
kubectl -n applications wait --for=condition=Ready secretstore/moco --timeout=2m
kubectl -n applications wait --for=condition=Ready externalsecret/database --timeout=2m
```

Mount the resulting `database` Secret in your workload using key `password`.
ESO decodes the Base64 API value exactly once. Do not add `urlquery` to the URL
template: the webhook provider already escapes `remoteRef.key`.

## Updates and failures

After updating the value in Moco, ESO refreshes the Kubernetes Secret on its
configured interval. A volume-mounted Secret updates asynchronously; environment
variables require a workload restart. An application may also need to reload
its credentials after the file changes.

Inspect failures without printing the secret value:

```sh
kubectl -n applications describe secretstore moco
kubectl -n applications describe externalsecret database
```

For `401`/`403`, check the token, tenant/vault IDs and reader policy. For TLS
errors, check the CA and hostname. For connection errors, check the Service and
NetworkPolicy. Update `moco-reader` when rotating the reader credential and
confirm ESO becomes Ready again. With `deletionPolicy: Retain`, an upstream
failure does not erase the last delivered value; monitor Ready conditions so
applications do not silently keep using stale credentials.
