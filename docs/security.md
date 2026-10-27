# Access and encryption keys

Use HTTPS and separate tokens for administrators, the controller, ESO and
applications. Give each principal only the tenants, vaults, paths and methods
it needs. Moco denies requests without an allow policy.

## Grant access

The production bootstrap creates `access.json` with token digests. To add a
reader, generate its token into a protected file and calculate the SHA-256 digest:

```sh
umask 077
openssl rand -hex 32 | tr -d '\n' > /private/moco/eso-reader-token
openssl dgst -sha256 /private/moco/eso-reader-token
```

Add an entry to the existing `principals` array in `access.json`, preserving
other principals. Copy only the printed hex digest into `tokenSha256`:

```json
{"id":"eso-reader","tokenSha256":"REPLACE_WITH_64_LOWERCASE_HEX_CHARACTERS"}
```

Restart the server after changing principal identities or token digests. With
Helm, update the credential Secret and bump `credentials.revision` to recopy it.
Deliver the clear token only to the intended client.

On first boot, Moco imports policies from `access.json`. After that, policies
live in SQLite: editing the bootstrap file does not replace them. Use the
private authorization API or [MocoAuthorization](operations/kubernetes-controller.md)
to manage the complete policy snapshot.

A reader policy for one vault can look like this; replace both IDs:

```json
{
  "subject":"eso-reader",
  "domain":"TENANT_ID",
  "path":"/api/v1/tenants/TENANT_ID/vaults/VAULT_ID/secret",
  "method":"GET",
  "secretPathPrefix":"apps/"
}
```

This allows value reads under `apps/`, not writes or other vaults. Add separate
policies if the client also needs metadata or listing. Path-scoped lists must
supply a nonempty `prefix` within the allowed prefix. `HEAD` uses `GET` permission.
Keep `domain: "*"` and wildcard methods limited to principals that need them.

## Change permissions safely

1. Use an administrator explicitly allowed to `GET` and `PUT`
   `/internal/v1/authorization` on the private admin listener.
2. Read the current snapshot and its ETag.
3. Prepare a JSON body containing the complete `roleBindings` and `policies`
   arrays. Preserve administrator and controller permissions you still need.
4. PUT the new body with `If-Match` set to the ETag. A `412` requires reading
   and reviewing the current snapshot again.
5. Verify both an allowed request and a denied request using the client token.

The [API reference](https://moco.araihu.com/openapi/) provides the exact body
schema. A replacement removes policies omitted from those arrays. Avoid mixing
manual replacements and an authoritative authorization CR.

To rotate a client token, replace its digest in `access.json`, restart/roll out
the server, update the client credential and verify the old token is rejected.
Coordinate the rollout: replacing a digest invalidates the old credential.

## Rotate the root encryption key

Root keys encrypt vault keys. Losing them makes the stored values and dependent
backups unrecoverable. Keep the keyring outside Git and separately from backups.
A keyring file uses this shape, with Base64-encoded 32-byte keys:

```json
{
  "activeKeyId":"root-v2",
  "keys":{"root-v1":"OLD_KEY_BASE64","root-v2":"NEW_KEY_BASE64"}
}
```

1. Back up the database and preserve the current keyring independently.
2. Add the new key and make its ID active, keeping the old keys. Increase
   `MOCO_ENCRYPTION_KEY_EPOCH`; with Helm use `encryption.epoch`.
3. Restart the single server and backup worker with the new keyring. With Helm,
   update the Secret and bump `credentials.revision`.
4. Call `POST /internal/v1/encryption/rotation` using a principal explicitly
   allowed to that route. Continue with both returned tenant/vault checkpoints
   while `hasMore` is true. Start fresh sweeps until `complete=true` and
   `remainingOldKeys=0`.
5. Check `GET /internal/v1/encryption/status`, make a fresh backup and rehearse a
   restore before removing an old key from the live keyring.

Keep old keys in recovery custody until every retained backup requiring them
expires. Do not reuse key IDs for different key bytes. Do not configure a
keyring and the legacy single `MOCO_ENCRYPTION_KEY` setting together.

## TLS and sensitive files

Use certificates covering the public hostname and the probe/administrative
names used by your deployment. Supply the CA to CLI/ESO clients; never disable
certificate checks to work around a mismatch. Restart after replacing certificate
files. Keep private directories at `0700` and credential/key files at `0600`.

SQLite metadata, audit records and backup metadata remain readable even though
secret values are encrypted. Protect volumes, archives and read output. See
[backup and recovery](operations/backup-restore.md) and
[audit operations](operations/transactional-audit.md) for the corresponding procedures.
