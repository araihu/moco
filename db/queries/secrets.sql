-- name: GetVaultKey :one
SELECT root_key_id, salt, wrapped_key, created_at
FROM vault_keys
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
LIMIT 1;

-- name: InsertVaultKey :execrows
INSERT INTO vault_keys (
    tenant_id,
    vault_id,
    root_key_id,
    salt,
    wrapped_key,
    created_at
) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (tenant_id, vault_id) DO NOTHING;

-- name: InsertSecret :one
INSERT INTO secrets (
    tenant_id,
    vault_id,
    path,
    salt,
    ciphertext,
    digest,
    content_type,
    version,
    created_at,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT (tenant_id, vault_id, path) DO NOTHING
RETURNING sequence, tenant_id, vault_id, path, salt, ciphertext, digest,
          content_type, version, created_at, updated_at;

-- name: UpsertSecret :one
INSERT INTO secrets (
    tenant_id,
    vault_id,
    path,
    salt,
    ciphertext,
    digest,
    content_type,
    version,
    created_at,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?)
ON CONFLICT (tenant_id, vault_id, path) DO UPDATE
SET salt = excluded.salt,
    ciphertext = excluded.ciphertext,
    digest = excluded.digest,
    content_type = excluded.content_type,
    version = secrets.version + 1,
    updated_at = excluded.updated_at
WHERE secrets.digest <> excluded.digest
   OR secrets.content_type <> excluded.content_type
RETURNING sequence, tenant_id, vault_id, path, salt, ciphertext, digest,
          content_type, version, created_at, updated_at;

-- name: UpdateSecretIfVersion :one
UPDATE secrets
SET salt = sqlc.arg(salt),
    ciphertext = sqlc.arg(ciphertext),
    digest = sqlc.arg(digest),
    content_type = sqlc.arg(content_type),
    version = version + 1,
    updated_at = sqlc.arg(updated_at)
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
  AND path = sqlc.arg(path)
  AND version = sqlc.arg(expected_version)
  AND (digest <> sqlc.arg(digest) OR content_type <> sqlc.arg(content_type))
RETURNING sequence, tenant_id, vault_id, path, salt, ciphertext, digest,
          content_type, version, created_at, updated_at;

-- name: GetSecretMetadata :one
SELECT sequence, tenant_id, vault_id, path, digest, content_type, version,
       created_at, updated_at
FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
  AND path = sqlc.arg(path)
LIMIT 1;

-- name: GetSecretRecord :one
SELECT secrets.sequence,
       secrets.tenant_id,
       secrets.vault_id,
       secrets.path,
       secrets.salt AS secret_salt,
       secrets.ciphertext,
       secrets.digest,
       secrets.content_type,
       secrets.version,
       secrets.created_at,
       secrets.updated_at,
       vault_keys.root_key_id,
       vault_keys.salt AS key_salt,
       vault_keys.wrapped_key,
       vault_keys.created_at AS key_created_at
FROM secrets
JOIN vault_keys
  ON vault_keys.tenant_id = secrets.tenant_id
 AND vault_keys.vault_id = secrets.vault_id
WHERE secrets.tenant_id = sqlc.arg(tenant_id)
  AND secrets.vault_id = sqlc.arg(vault_id)
  AND secrets.path = sqlc.arg(path)
LIMIT 1;

-- name: SecretSnapshotUpperBound :one
SELECT
    CAST(EXISTS(
        SELECT 1
        FROM vaults
        WHERE vaults.tenant_id = sqlc.arg(tenant_id)
          AND vaults.id = sqlc.arg(vault_id)
    ) AS INTEGER) AS vault_exists,
    CAST(COALESCE(MAX(sequence), 0) AS INTEGER) AS max_sequence
FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id);

-- name: ListSecretMetadataPage :many
SELECT sequence, tenant_id, vault_id, path, digest, content_type, version,
       created_at, updated_at
FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
  AND sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(snapshot_sequence)
  AND (
      sqlc.narg(prefix) IS NULL
      OR substr(path, 1, length(sqlc.narg(prefix))) = sqlc.narg(prefix)
  )
ORDER BY sequence
LIMIT sqlc.arg(page_size);

-- name: DeleteSecret :execrows
DELETE FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
  AND path = sqlc.arg(path);

-- name: DeleteSecretIfVersion :execrows
DELETE FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id)
  AND path = sqlc.arg(path)
  AND version = sqlc.arg(expected_version);

-- name: CountVaultSecrets :one
SELECT COUNT(*)
FROM secrets
WHERE tenant_id = sqlc.arg(tenant_id)
  AND vault_id = sqlc.arg(vault_id);
