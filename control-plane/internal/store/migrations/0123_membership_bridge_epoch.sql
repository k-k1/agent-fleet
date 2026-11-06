-- Bridge token epoch (issue #3452): a per-membership epoch mixed into the HMAC of every
-- per-membership bridge token (memo, schedule, MCP, docs, engine, git OAuth, AWS and GCP
-- profiles, branch rules). Removal, re-activation and an administrator's rotation advance it,
-- which kills every token minted under an earlier value. Default 0 is the epoch whose token
-- is byte-for-byte the token minted before this column existed, so the upgrade invalidates
-- nothing. bridge_epoch_started is the epoch the workspace's container was last started
-- under, so the Console can tell a running container holds tokens that are now dead.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE membership ADD COLUMN bridge_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE membership ADD COLUMN bridge_epoch_started INTEGER NOT NULL DEFAULT 0
