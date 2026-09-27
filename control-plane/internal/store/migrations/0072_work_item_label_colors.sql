-- The tracker's own label colours for a cached work item row, as a JSON object of
-- label name -> "rrggbb" (only GitHub has them). A separate column rather than a change to
-- `labels`, so the comma-separated names stay readable by an older Control Plane. Empty
-- until the row's query is next refreshed, and the Console then derives a colour from the name.
ALTER TABLE work_item_cache ADD COLUMN label_colors TEXT NOT NULL DEFAULT '';
