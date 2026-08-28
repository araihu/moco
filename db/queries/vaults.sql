-- name: InsertVault :one
INSERT INTO vaults (
    id,
    tenant_id,
    name,
    description,
    external_id,
    labels_json,
    revision,
    created_at,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING sequence, id, tenant_id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: GetVault :one
SELECT sequence, id, tenant_id, name, description, external_id, labels_json,
       revision, created_at, updated_at
FROM vaults
WHERE vaults.tenant_id = sqlc.arg(tenant_id)
  AND vaults.id = sqlc.arg(id)
LIMIT 1;

-- name: FindVaultConflict :one
SELECT id
FROM vaults
WHERE tenant_id = sqlc.arg(tenant_id)
  AND (
      name = sqlc.arg(name)
      OR (sqlc.narg(external_id) IS NOT NULL AND external_id = sqlc.narg(external_id))
  )
ORDER BY CASE WHEN name = ?2 THEN 0 ELSE 1 END
LIMIT 1;

-- name: VaultSnapshotUpperBound :one
SELECT
    CAST(EXISTS(SELECT 1 FROM tenants WHERE tenants.id = sqlc.arg(tenant_id)) AS INTEGER) AS tenant_exists,
    CAST(COALESCE(MAX(sequence), 0) AS INTEGER) AS max_sequence
FROM vaults
WHERE tenant_id = sqlc.arg(tenant_id);

-- name: ListVaultsPage :many
SELECT sequence, id, tenant_id, name, description, external_id, labels_json,
       revision, created_at, updated_at
FROM vaults
WHERE tenant_id = sqlc.arg(tenant_id)
  AND sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(snapshot_sequence)
  AND (sqlc.narg(name) IS NULL OR name = sqlc.narg(name))
  AND (sqlc.narg(external_id) IS NULL OR external_id = sqlc.narg(external_id))
ORDER BY sequence
LIMIT sqlc.arg(page_size);

-- name: UpdateVault :one
UPDATE vaults
SET name = sqlc.arg(name),
    description = sqlc.narg(description),
    labels_json = sqlc.arg(labels_json),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE tenant_id = sqlc.arg(tenant_id)
  AND id = sqlc.arg(id)
RETURNING sequence, id, tenant_id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: UpdateVaultIfRevision :one
UPDATE vaults
SET name = sqlc.arg(name),
    description = sqlc.narg(description),
    labels_json = sqlc.arg(labels_json),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE tenant_id = sqlc.arg(tenant_id)
  AND id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision)
RETURNING sequence, id, tenant_id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: DeleteVault :execrows
DELETE FROM vaults
WHERE tenant_id = sqlc.arg(tenant_id)
  AND id = sqlc.arg(id);

-- name: DeleteVaultIfRevision :execrows
DELETE FROM vaults
WHERE vaults.tenant_id = sqlc.arg(tenant_id)
  AND vaults.id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision);

-- name: DeleteVaultIfEmpty :execrows
DELETE FROM vaults
WHERE vaults.tenant_id = sqlc.arg(tenant_id)
  AND vaults.id = sqlc.arg(id)
  AND NOT EXISTS (
      SELECT 1
      FROM secrets
      WHERE secrets.tenant_id = vaults.tenant_id
        AND secrets.vault_id = vaults.id
  );

-- name: DeleteVaultIfRevisionAndEmpty :execrows
DELETE FROM vaults
WHERE vaults.tenant_id = sqlc.arg(tenant_id)
  AND vaults.id = sqlc.arg(id)
  AND vaults.revision = sqlc.arg(expected_revision)
  AND NOT EXISTS (
      SELECT 1
      FROM secrets
      WHERE secrets.tenant_id = vaults.tenant_id
        AND secrets.vault_id = vaults.id
  );
