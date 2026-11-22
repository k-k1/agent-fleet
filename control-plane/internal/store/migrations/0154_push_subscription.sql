-- Web Push subscriptions (#4750 part 1): one row per browser install a member opted in on.
-- Additive only. The endpoint is unique across members: a browser that signs in as someone else
-- re-subscribes with the same endpoint and the row moves to them, so the previous member stops
-- receiving pushes on it. id is derived from the endpoint (store_push.go) so the Console can name a
-- device without ever being shown the endpoint, which is a bearer URL for that browser.
CREATE TABLE push_subscription (
  id TEXT PRIMARY KEY,
  membership_id TEXT NOT NULL REFERENCES membership(id) ON DELETE CASCADE,
  endpoint TEXT NOT NULL UNIQUE,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL,
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_ok_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_push_subscription_membership ON push_subscription(membership_id);
