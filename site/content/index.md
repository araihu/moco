---
title: Mocó secret management for your homelab
description: Store application secrets on your own infrastructure. Manage them through the API, CLI or Kubernetes controller, and deliver values to workloads with External Secrets Operator.
language: en
layout:
  kind: landing
  values:
    shell: true
    navigation: [getting-started/index.md, guides/index.md, reference/index.md]
    navigation_label: Documentation
---
# Secrets for your homelab

Store passwords, tokens and application credentials on your own infrastructure.
Manage them through the API or command line, or declare tenants, vaults and
access policies in Kubernetes.

- [Try Moco locally](getting-started/index.md)
- [Install Moco](guides/installation/index.md)

![The API, CLI and Kubernetes controller manage Moco resources. Moco encrypts secret values in local SQLite. External Secrets Operator reads permitted values and creates Kubernetes Secrets for applications.](media/architecture.svg)

## Manage secrets through the API or CLI

Organize secrets into tenants and vaults, then address each secret with a path
such as `prod/database/password`. Read metadata separately from values. Use
conditional updates to avoid overwriting a newer version.

The CLI uses commands such as `moco tenants list`. Tables are the default;
choose JSON for scripts or CSV for tabular exports.
[Read the CLI guide](guides/cli/index.md) or [browse the API reference](/openapi/).

## Declare resources and access in Kubernetes

Keep tenants, vaults and access policies in Git using `MocoTenant`, `MocoVault`
and `MocoAuthorization`. The Moco controller reconciles these resources and
repairs drift. [Set up the controller](guides/controller/index.md).

External Secrets Operator, or ESO, reads permitted values through its webhook
provider and creates Kubernetes Secrets for your applications.
[Connect Moco to ESO](guides/eso/index.md).

## Audit writes and recover your data

Moco encrypts secret values with a separate key for each vault and lets you
rotate the root key. Successful API changes and their audit records commit in
the same SQLite transaction. If Moco cannot write the audit record, it rolls
back the change.
[Read the audit guarantees](guides/audit/index.md).

Automatic backups capture the full database and copy verified archives to a
configured mirror. Restore offline with the required historical root keys,
which you keep separately from the backups.
[Configure backups and rehearse a restore](guides/backup-restore/index.md).

## Deploy Moco 0.0.3

Run one server with local SQLite using Docker Compose or Helm. Add the separate
controller when you want Kubernetes to manage resources and access. The released
server and controller images support Linux amd64 and arm64, and both Helm
charts pin their images by digest. [Install the release](guides/installation/index.md).

Keep SQLite data on a local filesystem and maintain an off-host backup copy.
The supported topology has one writer, with downtime during recovery.
[Follow the production runbook](guides/production/index.md).

The published v0.0.3 images and charts passed 21 Kubernetes integration and
recovery scenarios, including token rotation, root-key rotation and a database
restore followed by fresh secret delivery through ESO.
[Review the test results and limits](reference/release-0.0.3/index.md).
