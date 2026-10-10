-- A generation for the home key's confirm step (issue #1646, part B).
--
-- RemigrateHomeDEK bumps it and ConfirmHomeDEK requires the value the start read, so an
-- Agent report taken before an operator put a home back to migrating (a copy restored from
-- before its store moved) cannot confirm it again: membership, ciphertext and scheme all
-- match again after the remigrate, and only this tells the two apart.
ALTER TABLE home_dek ADD COLUMN confirm_epoch INTEGER NOT NULL DEFAULT 0
