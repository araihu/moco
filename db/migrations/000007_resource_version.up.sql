CREATE TABLE moco_resource_version (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL CHECK (revision >= 0)
);

INSERT INTO moco_resource_version (id, revision)
VALUES (1, 0);

CREATE TRIGGER moco_resource_version_tenants_insert
AFTER INSERT ON tenants
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_tenants_update
AFTER UPDATE ON tenants
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_tenants_delete
AFTER DELETE ON tenants
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vaults_insert
AFTER INSERT ON vaults
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vaults_update
AFTER UPDATE ON vaults
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vaults_delete
AFTER DELETE ON vaults
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vault_keys_insert
AFTER INSERT ON vault_keys
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vault_keys_update
AFTER UPDATE ON vault_keys
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_vault_keys_delete
AFTER DELETE ON vault_keys
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_secrets_insert
AFTER INSERT ON secrets
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_secrets_update
AFTER UPDATE ON secrets
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_secrets_delete
AFTER DELETE ON secrets
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_role_bindings_insert
AFTER INSERT ON authorization_role_bindings
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_role_bindings_update
AFTER UPDATE ON authorization_role_bindings
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_role_bindings_delete
AFTER DELETE ON authorization_role_bindings
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_policies_insert
AFTER INSERT ON authorization_policies
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_policies_update
AFTER UPDATE ON authorization_policies
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_policies_delete
AFTER DELETE ON authorization_policies
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;

CREATE TRIGGER moco_resource_version_authorization_policy_state_update
AFTER UPDATE ON authorization_policy_state
BEGIN
    UPDATE moco_resource_version SET revision = revision + 1 WHERE id = 1;
END;
