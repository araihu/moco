CREATE TABLE encryption_key_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    active_root_key_id TEXT NOT NULL CHECK (length(active_root_key_id) <= 128),
    epoch INTEGER NOT NULL CHECK (epoch >= 0)
);

INSERT INTO encryption_key_state (id, active_root_key_id, epoch)
VALUES (1, '', 0);
