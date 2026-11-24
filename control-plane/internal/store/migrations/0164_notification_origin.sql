-- Who started the session a notification is about (#5081): the root of the chain (own, autorun,
-- schedule) and whether the session itself was started by another session. Additive. A row from
-- before, or from an Agent that does not send them, reads as the member's own and not a child.
ALTER TABLE notification ADD COLUMN origin_root TEXT NOT NULL DEFAULT 'own';
ALTER TABLE notification ADD COLUMN origin_child INTEGER NOT NULL DEFAULT 0;
