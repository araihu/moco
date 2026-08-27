-- name: InsertTenant :one
INSERT INTO tenants (
    id,
    name,
    description,
    external_id,
    labels_json,
    revision,
    created_at,
    updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING sequence, id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: GetTenant :one
SELECT sequence, id, name, description, external_id, labels_json,
       revision, created_at, updated_at
FROM tenants
WHERE id = ?
LIMIT 1;

-- name: FindTenantConflict :one
SELECT id
FROM tenants
WHERE name = sqlc.arg(name)
   OR (sqlc.narg(external_id) IS NOT NULL AND external_id = sqlc.narg(external_id))
ORDER BY CASE WHEN name = ?1 THEN 0 ELSE 1 END
LIMIT 1;

-- name: MaxTenantSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER)
FROM tenants;

-- name: ListTenantsPage :many
SELECT sequence, id, name, description, external_id, labels_json,
       revision, created_at, updated_at
FROM tenants
WHERE sequence > sqlc.arg(after_sequence)
  AND sequence <= sqlc.arg(snapshot_sequence)
  AND (sqlc.narg(name) IS NULL OR name = sqlc.narg(name))
  AND (sqlc.narg(external_id) IS NULL OR external_id = sqlc.narg(external_id))
ORDER BY sequence
LIMIT sqlc.arg(page_size);

-- name: UpdateTenant :one
UPDATE tenants
SET name = sqlc.arg(name),
    description = sqlc.narg(description),
    labels_json = sqlc.arg(labels_json),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
RETURNING sequence, id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: UpdateTenantIfRevision :one
UPDATE tenants
SET name = sqlc.arg(name),
    description = sqlc.narg(description),
    labels_json = sqlc.arg(labels_json),
    revision = revision + 1,
    updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision)
RETURNING sequence, id, name, description, external_id, labels_json,
          revision, created_at, updated_at;

-- name: DeleteTenant :execrows
DELETE FROM tenants
WHERE id = ?;

-- name: DeleteTenantIfRevision :execrows
DELETE FROM tenants
WHERE id = sqlc.arg(id)
  AND revision = sqlc.arg(expected_revision);

-- name: DeleteExpiredIdempotencyRecords :exec
DELETE FROM idempotency_records
WHERE expires_at <= ?;

-- name: GetIdempotencyRecord :one
SELECT principal_id, operation, idempotency_key, request_hash, status_code,
       resource_id, response_json, response_etag, created_at, expires_at
FROM idempotency_records
WHERE principal_id = sqlc.arg(principal_id)
  AND operation = sqlc.arg(operation)
  AND idempotency_key = sqlc.arg(idempotency_key)
LIMIT 1;

-- name: InsertIdempotencyRecord :execrows
INSERT INTO idempotency_records (
    principal_id,
    operation,
    idempotency_key,
    request_hash,
    status_code,
    resource_id,
    response_json,
    response_etag,
    created_at,
    expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (principal_id, operation, idempotency_key) DO NOTHING;
