-- Postgres mirror of migrations/0168_cp_oneoff.sql. The migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE cp_oneoff (
    name    TEXT PRIMARY KEY,
    done_at TEXT NOT NULL
);
