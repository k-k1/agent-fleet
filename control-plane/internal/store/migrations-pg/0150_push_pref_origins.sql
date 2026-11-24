-- Web Push origin switches (#5081): the origins (own, own-child, autorun, autorun-child,
-- schedule, schedule-child) a member turned off for push. Additive. An empty list is today's
-- behaviour, every origin pushed.
ALTER TABLE push_pref ADD COLUMN off_origins TEXT NOT NULL DEFAULT '[]';
