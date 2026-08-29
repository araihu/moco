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
