CREATE TABLE vaults (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    name TEXT NOT NULL,
    description TEXT,
    external_id TEXT,
    labels_json TEXT NOT NULL CHECK (json_valid(labels_json)),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (tenant_id) REFERENCES tenants (id) ON DELETE CASCADE,
    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, external_id)
);

CREATE INDEX vaults_tenant_sequence_idx
    ON vaults (tenant_id, sequence);
