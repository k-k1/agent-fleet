-- The last state seen for a pull request link (#3166): "open", "draft", "merged" or "closed",
-- '' when never seen or not a pull request. Mirrors the Agent's WorkItemLink.state so a stopped
-- Workspace's rows still draw a merged PR as done. Forward compatible: a new column with a
-- default, which an older Control Plane never reads.
ALTER TABLE session_work_link ADD COLUMN state TEXT NOT NULL DEFAULT ''
