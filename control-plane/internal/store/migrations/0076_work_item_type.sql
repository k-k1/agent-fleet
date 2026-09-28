-- The tracker's own issue type of a cached row (#1125): GitHub's issue type, Jira's issuetype.
-- The branch-name resolver maps it to a kind before the labels (ADR 0103 decision 4). '' when
-- the tracker has none, for a pull request, and for a row cached before this column existed.
ALTER TABLE work_item_cache ADD COLUMN item_type TEXT NOT NULL DEFAULT '';
