-- Who the engine work was for (ADR 0079 open question 7).
--
-- engine_hourly next door says a GPU box was up from 03:00. It cannot say WHO it was up for:
-- it has no membership axis, because until borrowing there was always somewhere else to look
-- -- the member's own ledger, a file inside their Workspace (engine_usage.go). A deployment
-- that LENDS its engines has no such place. The borrowing membership is purpose-made and has
-- no Workspace at all (ADR 0079 decision 3), so every row the gateway builds for it is thrown
-- away at engine_usage.go's `res.rt == nil` branch, and the operator who paid for the card is
-- left with a bill and no name.
--
-- Two tables because a conversation and a picture are not the same kind of fact, which is the
-- same line ADR 0079 decision 9 draws:
--
--   * engine_usage_undelivered -- the ROW, kept intact. It already exists, fully formed, at
--     the moment it is dropped: the gateway read the token counts out of the answer and put
--     the caller's session name on it. Nothing here counts anything new.
--   * engine_membership_hourly -- SECONDS AND REQUESTS, for the roles that have no tokens to
--     count. An image answer carries no `usage` object at all -- what it spends is pixels, and
--     the party that can see those is the Agent (ADR 0069 decision 9, ADR 0071 decision 9,
--     ADR 0076 decision 8 all say so, and the gateway refuses to write an `engine.image` row
--     for exactly that reason). Requests-per-membership-per-hour is the honest unit here.
--
-- ⚠️ NEITHER IS A SECOND LEDGER. ADR 0029's ledger is deliberately a file inside a Workspace,
-- it is what the usage graph reads, and nothing here feeds it. These answer one operator
-- question -- "whose work was that box doing" -- and the undelivered rows are NOT re-delivered
-- when a Workspace comes back: a row that arrived days late would land in the ledger under the
-- hour it was written rather than the hour it happened, and a ledger that quietly changes its
-- own past is worse than one with a hole an operator can see.
--
-- It is NOT money and must never be rendered as money -- the same rule as usage_hourly and
-- engine_hourly, for the same reason (ADR 0048 決定 2, ADR 0071 decision 9): no unit price is
-- attached to an engine token anywhere in this deployment.
CREATE TABLE engine_usage_undelivered (
  id            BIGSERIAL PRIMARY KEY,
  ts            TEXT NOT NULL,               -- RFC3339 (UTC) -- when the gateway gave up delivering it
  membership_id TEXT NOT NULL,               -- whose engine access was spent
  tenant_id     TEXT NOT NULL DEFAULT '',    -- denormalised so a tenant filter needs no join
  engine_key    TEXT NOT NULL,               -- 'llm' / 'image' -- the engine table's key
  reason        TEXT NOT NULL DEFAULT '',    -- 'no_workspace' / 'no_identity' / 'post_failed'
  feature       TEXT NOT NULL DEFAULT '',    -- the ledger's own vocabulary from here down
  provider      TEXT NOT NULL DEFAULT '',
  session       TEXT NOT NULL DEFAULT '',    -- the ledger's `ref` -- a BORROWER's when borrowed
  model         TEXT NOT NULL DEFAULT '',
  in_tokens     INTEGER NOT NULL DEFAULT 0,
  out_tokens    INTEGER NOT NULL DEFAULT 0,
  ms            INTEGER NOT NULL DEFAULT 0,
  ok            INTEGER NOT NULL DEFAULT 0,  -- 0/1 -- BOOLEAN is not used in either dialect here
  measured      TEXT NOT NULL DEFAULT ''     -- 'exact' / 'none', as the ledger means it
);
CREATE INDEX idx_engine_usage_undelivered_ts ON engine_usage_undelivered(ts);
CREATE INDEX idx_engine_usage_undelivered_membership ON engine_usage_undelivered(membership_id, ts);
CREATE TABLE engine_membership_hourly (
  engine_key    TEXT NOT NULL,               -- 'llm' / 'image'
  membership_id TEXT NOT NULL,
  hour          TEXT NOT NULL,               -- YYYY-MM-DDTHH (UTC) -- the client shifts to local time
  tenant_id     TEXT NOT NULL DEFAULT '',
  requests      INTEGER NOT NULL DEFAULT 0,  -- relayed requests, BOTH roles -- the image role's only count
  ok_requests   INTEGER NOT NULL DEFAULT 0,  -- of those, the ones the engine answered under 300
  ms            INTEGER NOT NULL DEFAULT 0,  -- summed round trip, engine time only
  in_tokens     INTEGER NOT NULL DEFAULT 0,  -- 0 for a role whose answers carry no usage object
  out_tokens    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (engine_key, membership_id, hour)
);
CREATE INDEX idx_engine_membership_hourly_hour ON engine_membership_hourly(hour);
