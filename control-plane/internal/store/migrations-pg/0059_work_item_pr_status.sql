-- CI and merge-conflict status of a cached pull request row (#1113). `checks` is a JSON object
-- {state,total,failed,pending} and `mergeable` is clean / conflict / unknown. Both are '' when
-- the Agent did not read them (issues, closed pull requests, Jira and Bitbucket rows, a row
-- cached before these columns existed), which the Console draws as no marker at all.
ALTER TABLE work_item_cache ADD COLUMN checks TEXT NOT NULL DEFAULT '';
ALTER TABLE work_item_cache ADD COLUMN mergeable TEXT NOT NULL DEFAULT '';
