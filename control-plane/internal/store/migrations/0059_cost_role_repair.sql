-- Repair: create cloud_cost_role_daily on a database that never got migration 0056.
--
-- Why a deployment can be missing it. Two branches wrote a migration with the SAME version
-- (0056 / pg 0041): one added this table, the other added engine_models. The engine one had
-- already been applied on the development deployment, so its version was recorded -- and when
-- the collision was resolved by renumbering the engine migration, the cost one arrived with a
-- version the database considered done and was SKIPPED IN SILENCE. The table it was supposed
-- to create was simply never there, and the first query against it is a 500 nobody expects.
--
-- IF NOT EXISTS everywhere: on every database that did get 0056 this file is a no-op, which is
-- what makes it safe to ship to deployments that were never damaged.
--
-- ⚠️ The rule this exists to enforce: NEVER RENUMBER A MIGRATION THAT HAS BEEN APPLIED
-- ANYWHERE. The version is a promise recorded in schema_migrations, and moving a file breaks
-- that promise in both directions at once -- the new number re-runs something already done,
-- and the old number swallows whatever takes its place.
CREATE TABLE IF NOT EXISTS cloud_cost_role_daily (
  day        TEXT NOT NULL,
  role       TEXT NOT NULL,
  service    TEXT NOT NULL,
  unblended  INTEGER NOT NULL DEFAULT 0,
  amortized  INTEGER NOT NULL DEFAULT 0,
  currency   TEXT NOT NULL DEFAULT '',
  estimated  INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, role, service)
);
CREATE INDEX IF NOT EXISTS idx_cloud_cost_role_day ON cloud_cost_role_daily(day);
