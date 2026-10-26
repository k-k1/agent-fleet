-- Comment count carried by the provider list response for a cached row (GitHub, Bitbucket).
-- -1 means unknown: every row cached before this column existed, a provider that does not say
-- (Jira) and an older Agent. It is never stored as 0, which would read as a known zero (#2779).
ALTER TABLE work_item_cache ADD COLUMN comments INTEGER NOT NULL DEFAULT -1;
