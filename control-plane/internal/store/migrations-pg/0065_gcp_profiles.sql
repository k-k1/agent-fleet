-- Postgres mirror of migrations/0080_gcp_profiles.sql.
CREATE TABLE IF NOT EXISTS gcp_profiles (
    id                          TEXT PRIMARY KEY,
    membership_id               TEXT NOT NULL,
    label                       TEXT NOT NULL,
    login_method                TEXT NOT NULL DEFAULT 'google',
    project                     TEXT NOT NULL,
    quota_project               TEXT NOT NULL DEFAULT '',
    account                     TEXT NOT NULL DEFAULT '',
    region                      TEXT NOT NULL DEFAULT '',
    zone                        TEXT NOT NULL DEFAULT '',
    impersonate_service_account TEXT NOT NULL DEFAULT '',
    created_at                  TEXT NOT NULL,
    updated_at                  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gcp_profiles_membership ON gcp_profiles(membership_id);
