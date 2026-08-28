CREATE UNIQUE INDEX vaults_tenant_id_id_idx
    ON vaults (tenant_id, id);

CREATE TABLE vault_keys (
    tenant_id TEXT NOT NULL,
    vault_id TEXT NOT NULL,
    root_key_id TEXT NOT NULL CHECK (length(root_key_id) BETWEEN 1 AND 128),
    salt BLOB NOT NULL CHECK (length(salt) = 32),
    wrapped_key BLOB NOT NULL CHECK (length(wrapped_key) = 48),
    created_at TEXT NOT NULL,
    PRIMARY KEY (tenant_id, vault_id),
    FOREIGN KEY (tenant_id, vault_id)
        REFERENCES vaults (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE secrets (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id TEXT NOT NULL,
    vault_id TEXT NOT NULL,
    path TEXT NOT NULL CHECK (length(path) BETWEEN 1 AND 1024),
    salt BLOB NOT NULL CHECK (length(salt) = 32),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) BETWEEN 17 AND 1048592),
    digest TEXT NOT NULL CHECK (
        length(digest) = 71
        AND substr(digest, 1, 7) = 'sha256:'
        AND substr(digest, 8) NOT GLOB '*[^0-9a-f]*'
    ),
    content_type TEXT NOT NULL CHECK (length(content_type) BETWEEN 1 AND 255),
    version INTEGER NOT NULL CHECK (version >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (tenant_id, vault_id, path),
    FOREIGN KEY (tenant_id, vault_id)
        REFERENCES vaults (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX secrets_vault_sequence_idx
    ON secrets (tenant_id, vault_id, sequence);
