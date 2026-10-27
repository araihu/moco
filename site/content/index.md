---
title: Mocó — secrets on your infrastructure
description: Self-hosted secret management with an API, a CLI and declarative Kubernetes resources. Encrypted SQLite, transactional audit and verified recovery.
language: en
layout:
  kind: landing
  values:
    shell: true
    navigation: [getting-started/index.md, guides/index.md, reference/index.md]
    navigation_label: Documentation
---
# Your secrets. Your infrastructure.

Manage secrets from your terminal. Declare access in Git. Deliver values to your
workloads with ESO. Mocó stores encrypted secret values in local SQLite — with audit
and recovery built in.

- [Get started](getting-started/index.md)
- [Explore the API](/openapi/)

![Moco connects an API and CLI or declarative Kubernetes resources to encrypted SQLite, then delivers scoped values through ESO to applications.](media/architecture.svg)

## One service. Three ways in.

**API.** Create tenants and vaults, address secrets by logical path and protect
updates with ETags. The public and administrative contracts are documented.

**CLI.** Use familiar commands such as `moco tenants list`. Read tables in your
terminal, JSON in scripts or CSV in a spreadsheet. [Use the CLI](guides/cli/index.md).

**GitOps.** Declare `MocoTenant`, `MocoVault` and `MocoAuthorization` in a trusted
namespace. The controller reconciles metadata and access; ESO delivers values.
[Meet the controller](guides/controller/index.md).

## Small footprint. Explicit boundaries.

One server, one SQLite writer. Per-vault envelope encryption. Default-deny
permissions scoped to tenants and secret-path prefixes. Successful mutations
commit with their audit event or roll back together.

Backups capture the full database, verify checksums and restore offline with the
required historical keys. Automatic mirrored snapshots and protected metrics
help you operate the service. [Read the production profile](guides/production/index.md).

## Released together. Tested together.

**v0.0.3** ships server and controller images for Linux amd64/arm64 and two Helm
charts with image digests pinned. The published artifacts passed 21 scenarios
with vind, Calico and ESO, including policy reconciliation, token and root-key
rotation, and restoration followed by a fresh ESO sync.

[Review the release evidence and its limits](reference/release-0.0.3/index.md).
This is a single-instance service. Validate off-host backup custody and recovery
on your own storage before keeping production secrets.

## Start where you are.

- [Run locally](getting-started/index.md) — start an API and try the CLI.
- [Install the release](guides/installation/index.md) — Docker Compose or Kubernetes.
- [Operate and recover](guides/index.md) — security, audit, backups and failure drills.
- [Read the source](https://github.com/araihu/moco) — Go modules and reviewed contracts.
