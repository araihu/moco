CREATE TABLE authorization_policy_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    initialized INTEGER NOT NULL CHECK (initialized IN (0, 1))
);

INSERT INTO authorization_policy_state (id, initialized)
VALUES (1, 0);
