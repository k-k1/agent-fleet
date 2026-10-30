-- Settings > AWS profiles/SSM "confirm before use" (ADR 0126 decision 4, issue #3050).
--
-- 1 makes af-aws-exec ask the member in the Console before the profile's first use in a
-- session. 0 (every row written before this migration) keeps today's behaviour. Not a secret.
-- Forward compatible: an older binary ignores the column and leaves its value alone.
ALTER TABLE ssm_profile ADD COLUMN confirm_use INTEGER NOT NULL DEFAULT 0
