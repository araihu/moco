-- name: GetEncryptionKeyState :one
SELECT active_root_key_id, epoch
FROM encryption_key_state
WHERE id = 1;

-- name: AdvanceEncryptionKeyState :execrows
UPDATE encryption_key_state
SET active_root_key_id = sqlc.arg(active_root_key_id),
    epoch = sqlc.arg(next_epoch)
WHERE id = 1
  AND epoch = sqlc.arg(expected_epoch);
