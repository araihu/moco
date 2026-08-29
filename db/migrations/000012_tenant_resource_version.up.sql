CREATE TABLE moco_tenant_resource_version (
    tenant_id TEXT PRIMARY KEY,
    revision INTEGER NOT NULL CHECK (revision >= 0)
);

-- Existing tenants start at a known checkpoint. Future mutations advance the
-- row through the triggers below; deleted tenants retain a tombstone row.
INSERT INTO moco_tenant_resource_version (tenant_id, revision)
SELECT id, 0
FROM tenants;

CREATE TRIGGER moco_tenant_resource_version_tenants_insert
AFTER INSERT ON tenants
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_tenants_update
AFTER UPDATE ON tenants
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_tenants_delete
AFTER DELETE ON tenants
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (OLD.id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_vaults_insert
AFTER INSERT ON vaults
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_vaults_update
AFTER UPDATE ON vaults
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_vaults_delete
AFTER DELETE ON vaults
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (OLD.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_secrets_insert
AFTER INSERT ON secrets
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_secrets_update
AFTER UPDATE ON secrets
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (NEW.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;

CREATE TRIGGER moco_tenant_resource_version_secrets_delete
AFTER DELETE ON secrets
BEGIN
    INSERT INTO moco_tenant_resource_version (tenant_id, revision)
    VALUES (OLD.tenant_id, 1)
    ON CONFLICT(tenant_id) DO UPDATE SET revision = revision + 1;
END;
