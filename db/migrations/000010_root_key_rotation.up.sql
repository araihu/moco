-- Rewrapping a vault data key changes only deployment encryption material, not
-- the logical API resource state observed by /api/v1/watch.
DROP TRIGGER IF EXISTS moco_resource_version_vault_keys_update;
