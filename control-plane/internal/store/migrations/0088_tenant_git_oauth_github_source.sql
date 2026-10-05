-- Which GitHub app a tenant's "connect with OAuth" button uses (issue #1667, ADR 0052
-- amendment). Only GitHub rows use these columns.
--
-- source is none, builtin_oauth, builtin_app or custom. The built-in apps' client_ids are
-- compiled into the binary, so a builtin row stores an empty client_id and a new release
-- can replace the app without rewriting rows. Rows written before this migration are
-- custom, which is what they were.
--
-- app_type is oauth_app or github_app for a custom client_id, empty when it could not be
-- told. app_type_by says how it was learnt - probe (GitHub answered at save time), token
-- (the prefix of a token minted through it) or prefix (a guess from the client_id). The
-- screen shows the last one as an estimate.
--
-- install_url is where a member installs a custom GitHub App. GitHub exposes no way to
-- find it from a client_id, so the tenant administrator enters it.
ALTER TABLE tenant_git_oauth ADD COLUMN source TEXT NOT NULL DEFAULT 'custom';
ALTER TABLE tenant_git_oauth ADD COLUMN app_type TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_git_oauth ADD COLUMN app_type_by TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_git_oauth ADD COLUMN install_url TEXT NOT NULL DEFAULT ''
