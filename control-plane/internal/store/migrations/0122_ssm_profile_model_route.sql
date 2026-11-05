-- Settings > AWS profiles/SSM "model route" mark (ADR 0128 decision 11 option C2, issue #3282).
--
-- 1 marks the profile as one the member lets the AF route gateway use for Amazon Bedrock model
-- calls, documented to carry only bedrock:InvokeModel*. 0 (every row written before this
-- migration) means the profile is never usable as a route. Not a secret.
-- Forward compatible: an older binary ignores the column and leaves its value alone.
ALTER TABLE ssm_profile ADD COLUMN model_route INTEGER NOT NULL DEFAULT 0
