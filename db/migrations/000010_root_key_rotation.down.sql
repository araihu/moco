CREATE TRIGGER IF NOT EXISTS moco_resource_version_vault_keys_update
AFTER UPDATE ON vault_keys
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;
