-- Internal git token rotation (issue #1199): a per-membership epoch mixed into the git
-- token's HMAC input. An administrator's "Rotate git token" bumps it, which kills every
-- token minted for an earlier value. Default 0 is the epoch whose token is byte-for-byte
-- the token minted before this column existed, so the upgrade invalidates nothing.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE membership ADD COLUMN git_token_epoch INTEGER NOT NULL DEFAULT 0
