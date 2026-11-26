-- Marker rows for start-time passes that must run once per database (#5212). Additive: a new table, nothing existing is touched.
CREATE TABLE cp_oneoff (
    name    TEXT PRIMARY KEY,
    done_at TEXT NOT NULL
);
