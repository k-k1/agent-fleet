-- Postgres mirror of migrations/0088_tenant_git_oauth_github_source.sql (issue #1667).
-- See the SQLite file for what each column means.
ALTER TABLE tenant_git_oauth ADD COLUMN source TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE tenant_git_oauth ADD COLUMN app_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_git_oauth ADD COLUMN app_type_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_git_oauth ADD COLUMN install_url TEXT NOT NULL DEFAULT ''
