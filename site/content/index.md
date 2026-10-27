---
title: Mocó secret management for your homelab
description: Store application secrets on your own infrastructure. Use the API or CLI, manage resources in Kubernetes and deliver values with External Secrets Operator.
language: en
layout:
  kind: landing
  values:
    shell: true
    navigation: [concepts/index.md, getting-started/index.md, guides/index.md, reference/index.md]
    navigation_label: Documentation
---
# Secrets for your homelab

Store passwords, tokens and application credentials on your own infrastructure.
Manage them through the API or CLI, or declare resources and permissions in
Kubernetes.

- [Create your first secret](getting-started/index.md)
- [How Moco works](concepts/index.md)

![The API, CLI and Kubernetes controller manage Moco resources. Moco encrypts values in local SQLite. External Secrets Operator reads permitted values and creates Kubernetes Secrets for applications.](media/architecture.svg)

## Organize secrets and control access

Create a tenant, add a vault and store values at paths such as
`apps/database/password`. Give each application a token with access only to
its own secrets. [Understand the model](concepts/index.md) or
[use the CLI](guides/cli/index.md).

## Deliver values to Kubernetes

Declare tenants, vaults and permissions with the
[Moco controller](guides/controller/index.md). Connect
[External Secrets Operator](guides/eso/index.md) to deliver values into
Kubernetes Secrets for your workloads.

## Run and recover your server

[Install Moco](guides/installation/index.md) on one Linux host or in Kubernetes.
It uses one writer and a local SQLite database. Keep off-host backups and the
encryption keys needed to restore them.

[Configure production](guides/production/index.md),
[manage access](guides/security/index.md) and
[rehearse recovery](guides/backup-restore/index.md).
