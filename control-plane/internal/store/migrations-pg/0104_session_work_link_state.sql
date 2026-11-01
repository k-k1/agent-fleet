-- Postgres mirror of migrations/0119_session_work_link_state.sql (#3166).
ALTER TABLE session_work_link ADD COLUMN state TEXT NOT NULL DEFAULT ''
