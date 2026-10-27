# Moco documentation

Start with [the local quick start](getting-started.md), then choose a
[deployment](installation.md). The public site at [moco.araihu.com](https://moco.araihu.com/)
publishes these repository guides using Margo and the OpenAPI reference using Manja.

| Task | Guide |
| --- | --- |
| Explore the API and CLI locally | [Get started](getting-started.md) |
| Install the released images and charts | [Installation](installation.md) |
| Use table, JSON and CSV CLI output | [CLI](../cli/README.md) |
| Configure TLS, credentials, metrics and automatic backups | [Production SQLite](operations/production-sqlite.md) |
| Configure the server chart | [Server Helm chart](../charts/moco/README.md) |
| Declare resources and adopt policies safely | [Controller](operations/kubernetes-controller.md) |
| Configure the controller chart | [Controller Helm chart](../charts/moco-controller/README.md) |
| Deliver secrets to workloads | [vind and ESO](operations/kubernetes-eso-tests.md) |
| Rotate keys and recover data | [Backup and restore](operations/backup-restore.md) |
| Understand transactional guarantees | [Audit](operations/transactional-audit.md) |
| Configure principals and policy scopes | [Security](security.md) |
| Run capacity and failure checks | [Load and faults](operations/load-fault-tests.md) |
| Understand the source and run checks | [Development](development.md) |
| Change API contracts and CLI metadata | [OpenAPI pipeline](../openapi/README.md) |
| Publish artifacts | [Releases](operations/releases.md) |
| Review verified 0.0.3 behavior | [Release evidence](releases/0.0.3.md) |
| Build and publish the website | [Site maintenance](../site/README.md) |

`docs/superpowers/` holds historical design notes and plans. Those documents are
not the supported operational instructions and are not published to the website.
