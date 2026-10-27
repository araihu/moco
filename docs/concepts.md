# How Moco works

Moco stores application secrets on your own infrastructure. You manage them
through the HTTP API or CLI. In Kubernetes, an optional controller manages
resources and permissions; External Secrets Operator (ESO) delivers values to
applications. There is no management UI or TUI.

## Tenants, vaults and secrets

| Resource | Purpose | Example |
| --- | --- | --- |
| Tenant | Separate a team or environment | `homelab` |
| Vault | Group secrets within a tenant | `applications` |
| Secret | Store a value at a logical path inside a vault | `apps/database/password` |

Create a tenant, create a vault in that tenant, then write a secret using both
IDs and a path. The same path can exist in different vaults. Names help people
identify resources; API requests use tenant and vault IDs.

A secret has a value, content type and version. Listing secrets returns metadata
only. Reading one secret returns its value encoded as Base64; decode it before
passing it to an application. Base64 is the API representation, not encryption.
Moco encrypts values before storing them in SQLite.

## Access and updates

Each client authenticates with a bearer token. An administrator grants its
principal access to specific tenants, vaults, path prefixes and HTTP methods.
Requests are denied unless a policy allows them. Give ESO a reader token and
applications only the permissions they need.

Use `If-None-Match: *` to create a secret only if it does not exist. To update an
existing value, read its ETag and send it in `If-Match`. A `412` response means
another writer changed the resource: read it again before retrying.

Successful changes commit together with an audit record. If that record cannot
be saved, the change fails. See [audit and troubleshooting](operations/transactional-audit.md).

## Choose a workflow

- **Terminal or scripts:** use the [CLI](../cli/README.md), or call the [HTTP API](https://moco.araihu.com/openapi/) directly.
- **GitOps:** declare tenants, vaults and permissions with the [controller](operations/kubernetes-controller.md).
- **Kubernetes applications:** connect [ESO](operations/eso.md) to copy permitted values into Kubernetes Secrets. The controller does not transport values.

Moco runs as one server with a local SQLite database. Keep the database on local
persistent storage and retain backups and encryption keys separately. Recovery
requires downtime; this version does not provide high availability or an external KMS.

[Create your first secret](getting-started.md), then choose an
[installation](installation.md) for persistent use.
