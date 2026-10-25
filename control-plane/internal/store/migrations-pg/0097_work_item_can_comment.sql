-- Whether af may post the report-back comment on a cached row. Set by the Agent for Bitbucket rows
-- only, from the scope the connection was granted (#2709). 0 for every row cached before this column
-- existed and for a row an older Agent sent, so those stay read-only until the next fetch says otherwise.
ALTER TABLE work_item_cache ADD COLUMN can_comment INTEGER NOT NULL DEFAULT 0;
