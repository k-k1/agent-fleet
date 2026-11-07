-- Notification event ids are unique per membership instead of deployment-wide (issue #3507).
-- A global UNIQUE let an id sent from one member's workspace take the id of a notification
-- the Control Plane meant for another member, which was then dropped by ON CONFLICT DO NOTHING.
-- Forward compatible: every existing row is copied as is, including seq, and the AUTOINCREMENT
-- counter is carried over so a swept tail does not hand out an old seq again.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE notification_new (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
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
INSERT INTO notification_new(seq, event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at)
  SELECT seq, event_id, membership_id, kind, target_type, target_id, target_kind, display_name, payload, created_at, seen_at FROM notification;
UPDATE sqlite_sequence SET seq=(SELECT MAX(seq) FROM sqlite_sequence WHERE name='notification')
  WHERE name='notification_new' AND (SELECT MAX(seq) FROM sqlite_sequence WHERE name='notification') > seq;
INSERT INTO sqlite_sequence(name, seq)
  SELECT 'notification_new', seq FROM sqlite_sequence
  WHERE name='notification' AND NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='notification_new');
DROP TABLE notification;
ALTER TABLE notification_new RENAME TO notification;
CREATE INDEX idx_notification_membership_seq ON notification(membership_id, seq DESC);
CREATE UNIQUE INDEX uq_notification_membership_event ON notification(membership_id, event_id)
