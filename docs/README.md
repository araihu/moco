# Moco documentation

Start with [how Moco works](concepts.md) and
[create your first secret](getting-started.md). The same guides are published
at [moco.araihu.com](https://moco.araihu.com/).

| Task | Guide |
| --- | --- |
| Understand tenants, vaults and secrets | [How Moco works](concepts.md) |
| Try Moco locally | [First secret](getting-started.md) |
| Install a persistent server | [Installation](installation.md) |
| Work from a terminal or script | [CLI](../cli/README.md) |
| Configure TLS, storage, metrics and upgrades | [Production profile](operations/production-sqlite.md) |
| Configure Helm | [Server chart](../charts/moco/README.md) · [Controller chart](../charts/moco-controller/README.md) |
| Declare tenants, vaults and permissions | [Controller](operations/kubernetes-controller.md) |
| Deliver values to workloads | [ESO](operations/eso.md) |
| Grant access and rotate keys | [Security](security.md) |
| Recover data | [Backup and restore](operations/backup-restore.md) |
| Read audit records and handle write failures | [Audit](operations/transactional-audit.md) |

Development, code generation, CI, acceptance suites, historical evidence and
publication procedures are collected under [Maintainers](maintainers.md).
Historical plans in `docs/superpowers/` are not user instructions.
