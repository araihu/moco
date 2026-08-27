CREATE TABLE tenants (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL UNIQUE,
    description TEXT,
    external_id TEXT UNIQUE,
    labels_json TEXT NOT NULL CHECK (json_valid(labels_json)),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE idempotency_records (
    principal_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status_code INTEGER NOT NULL,
    resource_id TEXT NOT NULL,
    response_json BLOB NOT NULL,
    response_etag TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (principal_id, operation, idempotency_key)
);

CREATE INDEX idempotency_records_expires_at_idx
    ON idempotency_records (expires_at);
