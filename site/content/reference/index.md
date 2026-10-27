---
title: Reference
description: Look up Moco API operations, Helm values and schemas, architecture, development checks and release results.
language: en
---
# Moco reference

Use the [API reference](/openapi/) to find request schemas, responses and errors.
It includes the public resource API and the administrative API for authorization,
audit and key rotation.

## Configure a deployment

- [Server Helm chart](server-chart/index.md), including storage, TLS, networking and backup values.
- [Controller Helm chart](controller-chart/index.md), including credentials, permissions and resource definitions.
- [Release 0.0.3 test results](release-0.0.3/index.md), including published artifact identities and recovery checks.

## Work on Moco

- [Architecture and development](development/index.md) for the source layout, toolchain and checks.
- [OpenAPI pipeline and CLI overlays](openapi-pipeline/index.md) for contract changes and command generation.
- [Release publication](releases/index.md) for images, Helm charts and attached checksums.

The software release, OpenAPI `info.version` and `/v1/` route namespace have
separate version histories. These guides describe the v0.0.3 runtime and the
repository's operational tooling.
