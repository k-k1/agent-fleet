-- Postgres mirror of migrations/0157_push_prefs.sql. The migrator splits on the semicolon, so comments must not contain one.
-- Web Push preferences (#4750 part 3). Additive only: no row means the current behaviour.
-- push_pref holds the notification kinds a member turned off for push, and a kind that is absent
-- is pushed. Stored per member rather than per device so a phone and a desktop agree.
CREATE TABLE push_pref (
  membership_id TEXT PRIMARY KEY REFERENCES membership(id) ON DELETE CASCADE,
  off_kinds TEXT NOT NULL DEFAULT '[]',
  updated_at TEXT NOT NULL
);
-- tenant_push_setting: whether a push may carry a question's text through the push services.
-- Off unless a tenant admin turns it on.
CREATE TABLE tenant_push_setting (
  tenant_id TEXT PRIMARY KEY REFERENCES tenant(id) ON DELETE CASCADE,
  include_text INTEGER NOT NULL DEFAULT 0,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);
