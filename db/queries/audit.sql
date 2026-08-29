-- name: InsertAuditEvent :one
INSERT INTO audit_events (
    occurred_at,
    request_id,
    principal_id,
    method,
    route,
    status_code,
    outcome,
    secret_path_sha256
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING sequence, occurred_at, request_id, principal_id, method, route,
          status_code, outcome, secret_path_sha256;

-- name: ListAuditEventsPage :many
SELECT sequence, occurred_at, request_id, principal_id, method, route,
       status_code, outcome, secret_path_sha256
FROM audit_events
WHERE sequence > sqlc.arg(after_sequence)
ORDER BY sequence
LIMIT sqlc.arg(page_size);

-- name: CurrentAuditSequence :one
SELECT CAST(COALESCE(MAX(sequence), 0) AS INTEGER)
FROM audit_events;

-- name: PurgeAuditEvents :execrows
DELETE FROM audit_events
WHERE sequence IN (
    SELECT candidates.sequence
    FROM audit_events AS candidates
    WHERE candidates.occurred_at < sqlc.arg(before_occurred_at)
    ORDER BY candidates.occurred_at, candidates.sequence
    LIMIT sqlc.arg(page_size)
);

-- name: CountAuditEventsBefore :one
SELECT COUNT(*)
FROM audit_events
WHERE occurred_at < sqlc.arg(before_occurred_at);
