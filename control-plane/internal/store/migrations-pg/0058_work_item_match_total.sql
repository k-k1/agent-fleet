-- How many rows a saved query matched when the last successful fetch could not carry them all
-- (#1095). 0 means the page held every match, a positive value is the tracker's own count, and
-- -1 means the page was full but the tracker did not say how many there were. Lives on the query
-- row so a stopped Workspace still shows the note next to the cached rows.
ALTER TABLE work_item_query ADD COLUMN match_total INTEGER NOT NULL DEFAULT 0;
