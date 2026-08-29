-- name: GetResourceVersion :one
SELECT revision
FROM moco_resource_version
WHERE id = 1;
