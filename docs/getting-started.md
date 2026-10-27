# Get started locally

This guide runs a development server bound to localhost. For persistent workloads,
start with [installation](installation.md) and the [production profile](operations/production-sqlite.md).

## Start the server

Install Go 1.27.3 or newer, Make and OpenSSL. From a checkout of Moco:

```sh
export MOCO_ADDR=127.0.0.1:8080
export MOCO_BEARER_TOKEN="$(openssl rand -hex 32)"
export MOCO_CURSOR_HMAC_KEY="$(openssl rand -hex 32)"
export MOCO_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export MOCO_ENCRYPTION_KEY_ID=local-v1
make run
```

Keep this terminal open. Startup creates `./moco.db` and applies embedded migrations.
Do not regenerate the encryption key when restarting an existing database.
The development token grants full access; use dedicated principals for deployments.

## Build and use the CLI

In another terminal at the repository root:

```sh
make cli-build
export MOCO_SERVER=http://127.0.0.1:8080
# Supply the same token from the first terminal through your credential mechanism.
# Set MOCO_TOKEN without committing it or including it in shell history.
bin/moco service info
bin/moco tenants list
printf '%s\n' '{"name":"homelab","labels":{}}' > tenant.json
bin/moco tenants create --body-file tenant.json --idempotency-key homelab-tenant-001
bin/moco tenants list --output json
bin/moco tenants list --output csv
```

`MOCO_TOKEN` is the client credential; `MOCO_BEARER_TOKEN` configures the development
server. They must contain the same value in this example. The CLI does not store
credentials. Its default table view is convenient for a terminal; JSON retains
the full response, including pagination. CSV emits records with headers.

## Create a vault and store a secret

Use the tenant ID from the create response:

```sh
printf '%s\n' '{"name":"application","labels":{}}' > vault.json
bin/moco vaults create --tenant-id TENANT --body-file vault.json \
  --idempotency-key homelab-vault-001
# Replace VAULT with the returned vault ID. This stores the synthetic value "hello".
printf '%s\n' '{"value":"aGVsbG8=","contentType":"text/plain"}' > secret.json
bin/moco secrets put --tenant-id TENANT --vault-id VAULT \
  --path prod/database/password --body-file secret.json --if-none-match '*'
bin/moco secrets get --tenant-id TENANT --vault-id VAULT --path prod/database/password
```

The value field is Base64-encoded, as defined by the API contract. This is wire
encoding, not encryption. Real secret material should come from a protected
file or pipe, rather than a command saved in shell history.

Mutation bodies come from `--body-file` or `--body-stdin`. Use `--if-match` with
the current ETag for conditional replacements. Review the [CLI guide](../cli/README.md)
and [API contracts](../openapi/README.md) for request schemas and flags.
Secret paths use a query parameter rather than an object-storage API. Metadata
listing never returns values; protect the output of value reads.

## Next steps

- [Install released OCI images and Helm charts](installation.md).
- [Declare tenants, vaults and policies with the controller](operations/kubernetes-controller.md).
- [Deliver values through ESO](operations/kubernetes-eso-tests.md).
- [Keep keys and verify recovery](operations/backup-restore.md).
