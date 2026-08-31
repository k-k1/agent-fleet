-- Memo image attachments (docs/log/21 画像添付): a memo can carry image files shared from a
-- phone (Android 共有シート) or dragged into the composer. attachments is a JSON array of
-- {path,name} objects where path is the in-container absolute path under
-- ~/.cache/agent-fleet/memo-images and name is the basename. '' means no attachments.
-- The image bytes live in the workspace container (reusing the paste-image store), NOT in
-- this DB — this column only references them, mirroring how kind=file uses ref_path.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo ADD COLUMN attachments TEXT NOT NULL DEFAULT '';
