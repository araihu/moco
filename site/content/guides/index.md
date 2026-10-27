---
title: Guides
description: Run Moco locally, install a release, manage secrets and access, and prepare backups and recovery.
language: en
---
# Use and operate Moco

[Start locally](../getting-started/index.md) to create a tenant, vault and secret
with the CLI. For a persistent deployment, choose Docker Compose or Kubernetes
in the [installation guide](installation/index.md).

## Manage secrets and access

- [Use the CLI](cli/index.md) for table, JSON and CSV output.
- [Configure the controller](controller/index.md) to declare tenants, vaults and access policies in Kubernetes.
- [Connect External Secrets Operator](eso/index.md) to deliver secret values to workloads.
- [Configure security and keys](security/index.md) for scoped permissions and root-key rotation.

## Run and recover a deployment

- [Run the production SQLite profile](production/index.md) with TLS, metrics and automatic backups.
- [Back up and restore the database](backup-restore/index.md), including backups that need historical keys.
- [Understand transactional audit](audit/index.md) and what happens when an audit write fails.
- [Run load and failure tests](load-faults/index.md) to check capacity and recovery behavior.
