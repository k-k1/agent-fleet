-- Schedule generation (#4834): a counter every config write, pause/resume and approval bumps, so an
-- approval or an edit that read the row earlier is refused when another write got in between.
-- Forward compatible: default 0 reads every existing row as generation 0.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN generation INTEGER NOT NULL DEFAULT 0
