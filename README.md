# Mocó

Self-hosted secret management for your homelab. Use the HTTP API or CLI directly,
or declare tenants, vaults and authorization in Kubernetes and deliver secret
values to applications with External Secrets Operator (ESO).

**Current release: [v0.0.3](https://github.com/araihu/moco/releases/tag/v0.0.3).**
[Website and documentation](https://moco.araihu.com/) ·
[Get started](docs/getting-started.md) · [Installation](docs/installation.md) ·
[Public and administrative API reference](https://moco.araihu.com/openapi/)

## What Moco does

- Isolates tenants and vaults; addresses secrets with logical paths such as `prod/database/password`.
- Encrypts values with per-vault data keys and AES-256-GCM envelope encryption; supports root-key rotation and rewrapping.
- Applies default-deny, tenant- and path-scoped authorization with revision-guarded policy snapshots.
- Commits successful mutations and audit events in the same SQLite transaction.
- Produces verified full backups with automatic mirrored snapshots, retention and offline recovery using historical keys.
- Provides a Cobra CLI with **table** output by default, plus **JSON** and **CSV**.
- Reconciles `MocoTenant`, `MocoVault` and `MocoAuthorization` through a dedicated Kubernetes controller.
- Exposes probes, protected Prometheus metrics and operational alerts.

Moco has an API, CLI and Kubernetes controller. It does not currently provide a
management UI or TUI. The website is documentation.

## Choose how to use it

| Workflow | Start here |
| --- | --- |
| Try the released server locally | [Local quick start](docs/getting-started.md) |
| Operate through a terminal | [CLI guide](cli/README.md) |
| Run one instance with Docker Compose | [Production SQLite profile](docs/operations/production-sqlite.md) |
| Deploy to Kubernetes with Helm | [Server chart](charts/moco/README.md) |
| Manage resources declaratively | [Controller and CRDs](docs/operations/kubernetes-controller.md) |
| Deliver secrets with ESO | [ESO guide](docs/operations/eso.md) |

## Get started

Read [how Moco works](docs/concepts.md), then
[create your first secret](docs/getting-started.md) with the released Docker image
and CLI. For persistent storage, follow [installation](docs/installation.md).

## Release 0.0.3

| Artifact | Published location |
| --- | --- |
| Server image (Linux amd64/arm64) | `ghcr.io/araihu/moco:0.0.3` |
| Controller image (Linux amd64/arm64) | `ghcr.io/araihu/moco-controller:0.0.3` |
| Server chart | `oci://ghcr.io/araihu/charts/moco`, version `0.0.3` |
| Controller chart | `oci://ghcr.io/araihu/charts/moco-controller`, version `0.0.3` |
| Checksums, manifest and both chart archives | [GitHub Release](https://github.com/araihu/moco/releases/tag/v0.0.3) |

Released charts pin their component's image digest. Credentials, certificates,
storage and backup mirror identity are operator inputs; the charts do not
invent them. Follow the [installation guide](docs/installation.md) before installing.

The supported production topology is **one writer with local SQLite**.
External KMS/HSM and high availability are not implemented. A mirror volume on
the same host is not an independent disaster-recovery copy. Validate off-host
custody, restore, capacity and alerts in your target environment.

## Documentation

- [User guides](docs/README.md)
- [Access and encryption keys](docs/security.md)
- [Backup and restore](docs/operations/backup-restore.md)
- [Audit and write failures](docs/operations/transactional-audit.md)

Development, code generation, CI, test evidence and publication belong in the
separate [Maintainers guide](docs/maintainers.md).
