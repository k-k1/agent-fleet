-- Comment count carried by the provider list response for a cached row (GitHub, Bitbucket). 0 for
-- every row cached before this column existed and for a provider that does not say (Jira), and
-- the next fetch fills it in (#2779).
ALTER TABLE work_item_cache ADD COLUMN comments INTEGER NOT NULL DEFAULT 0;
