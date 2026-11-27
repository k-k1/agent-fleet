ALTER TABLE tenant ADD COLUMN memo_run_limit INTEGER NOT NULL DEFAULT 0
    CHECK (memo_run_limit BETWEEN 0 AND 10);
ALTER TABLE user_limit ADD COLUMN memo_run_limit INTEGER NOT NULL DEFAULT 0
    CHECK (memo_run_limit BETWEEN 0 AND 10);
