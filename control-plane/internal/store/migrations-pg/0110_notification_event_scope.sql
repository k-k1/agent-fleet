-- Notification event ids are unique per membership (issue #3507), Postgres mirror of
-- migrations/0125_notification_event_scope.sql. See that file for the reason.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE notification DROP CONSTRAINT IF EXISTS notification_event_id_key;
CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_membership_event ON notification(membership_id, event_id)
