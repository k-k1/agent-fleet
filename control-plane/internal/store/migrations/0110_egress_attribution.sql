-- Egress attribution (ADR 0122, addendum 2026-10-24). egress_event keeps each gateway
-- observation as it arrived, keyed by the id the sender gives it, so a retried batch is
-- stored once. The source address is never rewritten: attribution is a join done at read
-- time against workspace_address, because a holder row can arrive after the event.
--
-- workspace_address is who held an address and when, as the orchestrator reported it.
-- Addresses are reused, so a row is an interval and never a bare address. until_at 0
-- means the address is still held. A row is one incarnation: ref (task ARN, pod UID,
-- container id) and from_at together tell a restart apart from the same holder seen again.
--
-- Times are unix milliseconds. Neither table is in membershipCascade: like audit_log they
-- are the record, and an offboarding must not erase who made a connection.
CREATE TABLE IF NOT EXISTS egress_event(
  id          TEXT PRIMARY KEY,
  at          BIGINT NOT NULL,
  source      TEXT NOT NULL,
  dest        TEXT NOT NULL DEFAULT '',
  host        TEXT NOT NULL DEFAULT '',
  allowed     INTEGER NOT NULL,
  count       INTEGER NOT NULL DEFAULT 1,
  received_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS egress_event_at ON egress_event(at);
CREATE TABLE IF NOT EXISTS workspace_address(
  membership_id TEXT NOT NULL,
  workspace_id  TEXT NOT NULL DEFAULT '',
  address       TEXT NOT NULL,
  ref           TEXT NOT NULL,
  from_at       BIGINT NOT NULL,
  until_at      BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY(ref, address, from_at)
);
CREATE INDEX IF NOT EXISTS workspace_address_addr ON workspace_address(address, from_at);
CREATE INDEX IF NOT EXISTS workspace_address_open ON workspace_address(membership_id, until_at);
