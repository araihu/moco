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
| Explore locally with Go and SQLite | [Local quick start](docs/getting-started.md) |
| Operate through a terminal | [CLI guide](cli/README.md) |
| Run one instance with Docker Compose | [Production SQLite profile](docs/operations/production-sqlite.md) |
| Deploy to Kubernetes with Helm | [Server chart](charts/moco/README.md) |
| Manage resources declaratively | [Controller and CRDs](docs/operations/kubernetes-controller.md) |
| Deliver secrets with ESO | [Kubernetes/ESO acceptance and examples](docs/operations/kubernetes-eso-tests.md) |

## Try it locally

Requires Go **1.27.3 or newer**, OpenSSL and Make. This development configuration
uses HTTP on localhost; use the [production profile](docs/operations/production-sqlite.md)
for TLS, separated credentials and persistent storage.

```sh
git clone https://github.com/araihu/moco.git
cd moco
export MOCO_ADDR=127.0.0.1:8080
export MOCO_BEARER_TOKEN="$(openssl rand -hex 32)"
export MOCO_CURSOR_HMAC_KEY="$(openssl rand -hex 32)"
export MOCO_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export MOCO_ENCRYPTION_KEY_ID=local-v1
make run
```

Keep the encryption key with separate recovery custody before retaining data.
In another terminal, [build the CLI and create your first tenant](docs/getting-started.md).
The database defaults to `./moco.db`; override it with `MOCO_DB_PATH`.

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

The published images and charts passed [21 acceptance scenarios with vind,
Calico and ESO](https://github.com/araihu/moco/actions/runs/40448153000), including
declarative reconciliation, scoped access, key/token rotation and restore.
See [the release validation record](docs/releases/0.0.3.md) for evidence and limits.

The supported production topology is **one writer with local SQLite**.
External KMS/HSM and high availability are not implemented. A mirror volume on
the same host is not an independent disaster-recovery copy. Validate off-host
custody, restore, capacity and alerts in your target environment.

## Documentation and development

- [Documentation map](docs/README.md)
- [Security, authorization and root-key management](docs/security.md)
- [Backup and restore](docs/operations/backup-restore.md)
- [Transactional audit guarantees](docs/operations/transactional-audit.md)
- [Architecture, repository layout and development checks](docs/development.md)
- [OpenAPI contracts, Vacuum overlays and CLI generation](openapi/README.md)
- [Release publication](docs/operations/releases.md)
- [Website source, build and deployment](site/README.md)

```sh
make check              # application, controller, CLI, lint, vulnerability scans and drills
make chart-check        # helm-unittest and chart integration checks
make vind-test          # published release acceptance with vind + ESO
make site-check         # Margo guides + Manja reference, export and link checks
```

Moco's CLI is the `cli/` module in this repository. The generic
[oapi-cli-gen](https://github.com/araihu/oapi-cli-gen) remains a separate project.
The site uses [Margo](https://github.com/araihu/margo) and
[Manja](https://github.com/araihu/manja), following the AraiHu documentation stack.
