# Create your first secret

This walkthrough uses the released server in a disposable Docker container.
You need Docker, OpenSSL and the [Moco CLI](../cli/README.md#install-the-cli).
Use sample values only: stopping this container removes its database.
For persistent storage, follow [installation](installation.md).

## Start Moco

Run these commands in one terminal so the server and CLI share the demo token:

```sh
export MOCO_TOKEN="$(openssl rand -hex 32)"
export MOCO_BEARER_TOKEN="$MOCO_TOKEN"
export MOCO_CURSOR_HMAC_KEY="$(openssl rand -hex 32)"
export MOCO_ENCRYPTION_KEY="$(openssl rand -base64 32)"
export MOCO_ENCRYPTION_KEY_ID=local-v1
export MOCO_SERVER=http://127.0.0.1:8080

docker run --detach --rm --name moco-demo \
  --publish 127.0.0.1:8080:8080 \
  --env MOCO_ADDR=0.0.0.0:8080 \
  --env MOCO_BEARER_TOKEN --env MOCO_CURSOR_HMAC_KEY \
  --env MOCO_ENCRYPTION_KEY --env MOCO_ENCRYPTION_KEY_ID \
  ghcr.io/araihu/moco:0.0.3

curl --fail "$MOCO_SERVER/readyz"
moco service info
```

If readiness is not available yet, check `docker logs moco-demo` and retry.
The demo listens only on host loopback and grants the token full access.

## Create a tenant and vault

```sh
printf '%s\n' '{"name":"homelab","labels":{}}' > tenant.json
moco tenants create --body-file tenant.json --idempotency-key demo-tenant-001
```

Copy the returned tenant ID. Set `TENANT` to that ID, then create a vault:

```sh
export TENANT=REPLACE_WITH_TENANT_ID
printf '%s\n' '{"name":"applications","labels":{}}' > vault.json
moco vaults create --tenant-id "$TENANT" --body-file vault.json \
  --idempotency-key demo-vault-001
```

Set `VAULT` to the returned vault ID:

```sh
export VAULT=REPLACE_WITH_VAULT_ID
```

Keep the same idempotency key if you need to retry a create request after a
connection failure. Use a new key for a different creation.

## Write and read a value

This stores the sample text `hello`, represented as Base64 in the request:

```sh
printf '%s\n' '{"value":"aGVsbG8=","contentType":"text/plain"}' > secret.json
moco secrets put --tenant-id "$TENANT" --vault-id "$VAULT" \
  --path apps/demo/password --body-file secret.json --if-none-match '*'
moco secrets get --tenant-id "$TENANT" --vault-id "$VAULT" \
  --path apps/demo/password
moco secrets list --tenant-id "$TENANT" --vault-id "$VAULT"
```

The read returns a Base64 value. The list returns metadata without values.
Keep real secret inputs in protected files or pipes, and keep read output out of logs.

## Stop the demo or continue

```sh
docker stop moco-demo
```

This deletes the demo database. For the next step, [install a persistent server](installation.md),
learn [CLI updates and output formats](../cli/README.md), or
[deliver a secret to Kubernetes with ESO](operations/eso.md).
