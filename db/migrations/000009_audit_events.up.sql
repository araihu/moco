CREATE TABLE audit_events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    request_id TEXT NOT NULL CHECK (length(request_id) BETWEEN 1 AND 128),
    principal_id TEXT CHECK (principal_id IS NULL OR length(principal_id) BETWEEN 1 AND 128),
    method TEXT NOT NULL CHECK (length(method) BETWEEN 1 AND 16),
    route TEXT NOT NULL CHECK (length(route) BETWEEN 1 AND 2048),
    status_code INTEGER NOT NULL CHECK (status_code BETWEEN 100 AND 599),
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failure')),
    secret_path_sha256 TEXT CHECK (
        secret_path_sha256 IS NULL
        OR (
            length(secret_path_sha256) = 64
            AND secret_path_sha256 NOT GLOB '*[^0-9a-f]*'
        )
    )
);

CREATE INDEX audit_events_occurred_at_idx
    ON audit_events (occurred_at, sequence);
