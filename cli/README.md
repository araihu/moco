# Use the Moco CLI

Use `moco` to manage tenants, vaults and secrets from a terminal or script.
The CLI connects to your server; it does not store credentials.

## Install the CLI

Release 0.0.3 ships server images and Helm charts. Install its CLI from the
released source using Git and Go 1.27.3 or newer:

```sh
git clone --branch v0.0.3 --depth 1 https://github.com/araihu/moco.git moco-cli-source
GOWORK=off go -C moco-cli-source/cli install ./cmd/moco
```

Put `GOBIN`, or `GOPATH/bin` when `GOBIN` is unset, on your `PATH`.
Confirm the command is available with `moco --help`.

## Connect to a server

```sh
export MOCO_SERVER=https://moco.example.com:8443
# Supply MOCO_TOKEN from your credential manager or a protected environment.
moco --ca-file /private/moco/ca.pem service info
moco --ca-file /private/moco/ca.pem tenants list
```

`MOCO_TOKEN` must be a token allowed by that server. Use `--ca-file` on each
request when the server uses a private CA; public certificates use system trust.
HTTP is allowed on loopback for the [local walkthrough](../docs/getting-started.md).

## Find commands and choose output

```sh
moco --help
moco tenants --help
moco secrets put --help
moco tenants list                  # table for a terminal
moco tenants list --output json    # full response, including pagination
moco tenants list --output csv     # records with a header row
```

Each list command returns one page. With JSON output, follow `nextCursor` using
`--cursor` until `hasMore` is false. Repeat the same filters on subsequent pages.
Use `--limit` to set the page size. CSV and table output do not remove pagination.

## Create resources and write secrets

[The walkthrough](../docs/getting-started.md#create-a-tenant-and-vault) covers
creating tenants and vaults. Supply JSON request bodies with
`--body-file request.json` or `--body-stdin`; schemas are in the
[API reference](https://moco.araihu.com/openapi/).

To replace a secret, first read its current ETag:

```sh
moco secrets get --tenant-id "$TENANT" --vault-id "$VAULT" \
  --path apps/demo/password --response-header ETag
moco secrets put --tenant-id "$TENANT" --vault-id "$VAULT" \
  --path apps/demo/password --body-file replacement.json \
  --if-match 'REPLACE_WITH_THE_COMPLETE_ETAG'
```

Preserve the ETag's quotes. `replacement.json` contains `value` in Base64 and
`contentType`. Use `--if-none-match '*'` instead when creating a new secret.
On `412`, read again before deciding whether to overwrite. A timeout does not
prove the write failed; read the current state before retrying.

## Common errors

| Result | Action |
| --- | --- |
| `401` | Check the token and whether it was rotated. |
| `403` | Ask for permission to the requested tenant, vault, path and method. |
| `404` | Check the server URL, tenant/vault IDs and logical path. |
| `412` | Refresh the ETag and review the newer value. |
| `503` | Check server readiness and the [operations guide](../docs/operations/production-sqlite.md). |
| Certificate error | Supply the correct CA and use a hostname listed in the certificate. |

Generate shell completion with `moco completion bash`, `zsh`, `fish` or `powershell`.
