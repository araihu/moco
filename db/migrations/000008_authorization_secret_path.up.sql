DROP TRIGGER IF EXISTS moco_resource_version_authorization_policies_delete;
DROP TRIGGER IF EXISTS moco_resource_version_authorization_policies_update;
DROP TRIGGER IF EXISTS moco_resource_version_authorization_policies_insert;
DROP INDEX IF EXISTS authorization_policies_subject_domain_idx;

ALTER TABLE authorization_policies RENAME TO authorization_policies_legacy;

CREATE TABLE authorization_policies (
    subject TEXT NOT NULL CHECK (length(subject) BETWEEN 1 AND 128),
    domain TEXT NOT NULL CHECK (length(domain) BETWEEN 1 AND 128),
    path TEXT NOT NULL CHECK (length(path) BETWEEN 7 AND 512),
    method TEXT NOT NULL CHECK (length(method) BETWEEN 1 AND 16),
    secret_path_prefix TEXT NOT NULL DEFAULT '' CHECK (length(secret_path_prefix) <= 1024),
    PRIMARY KEY (subject, domain, path, method, secret_path_prefix)
);

INSERT INTO authorization_policies (subject, domain, path, method)
SELECT subject, domain, path, method
FROM authorization_policies_legacy;

DROP TABLE authorization_policies_legacy;

CREATE INDEX authorization_policies_subject_domain_idx
    ON authorization_policies (subject, domain);

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
