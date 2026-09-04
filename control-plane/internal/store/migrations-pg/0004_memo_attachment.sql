-- Memo image attachments (docs/log/21 image attachments) — Postgres parity with sqlite 0021.
-- attachments is a JSON array of {path,name} referencing image bytes stored in the
-- workspace container under ~/.cache/agent-fleet/memo-images (not in this DB).
ALTER TABLE memo ADD COLUMN attachments TEXT NOT NULL DEFAULT '';
