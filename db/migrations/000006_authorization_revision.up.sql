ALTER TABLE authorization_policy_state
    ADD COLUMN revision INTEGER NOT NULL DEFAULT 0;
