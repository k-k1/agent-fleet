ALTER TABLE memo_delivery ADD COLUMN defer_session TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN defer_move_key TEXT NOT NULL DEFAULT '';
