-- name: GetResourceVersion :one
SELECT revision
FROM moco_resource_version
WHERE id = 1;

-- name: GetTenantResourceVersion :one
SELECT revision
FROM moco_tenant_resource_version
WHERE tenant_id = ?;
