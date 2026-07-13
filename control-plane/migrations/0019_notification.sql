CREATE TABLE notification (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL UNIQUE,
  membership_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  target_type TEXT NOT NULL DEFAULT '',
  target_id TEXT NOT NULL DEFAULT '',
  target_kind TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  seen_at TEXT NOT NULL DEFAULT '',
  FOREIGN KEY (membership_id) REFERENCES membership(id) ON DELETE CASCADE
);
CREATE INDEX idx_notification_membership_seq ON notification(membership_id, seq DESC);
CREATE TABLE notification_usage_state (
  membership_id TEXT NOT NULL,
  source TEXT NOT NULL,
  window_key TEXT NOT NULL,
  resets_at TEXT NOT NULL DEFAULT '',
  armed INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (membership_id, source, window_key),
  FOREIGN KEY (membership_id) REFERENCES membership(id) ON DELETE CASCADE
);
