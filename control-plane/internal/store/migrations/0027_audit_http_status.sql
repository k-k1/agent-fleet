-- Preserve the upstream HTTP outcome for audited proxy mutations whose
-- namespace effect cannot be inferred from success/failure alone (docs/log/44).
-- Existing rows use 0 = not recorded.
ALTER TABLE audit_log ADD COLUMN http_status INTEGER NOT NULL DEFAULT 0;
