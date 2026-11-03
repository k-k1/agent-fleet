-- ADR 0128 P1a: an optional explicit model route for the sessions a schedule creates.
-- route is a route id and route_connection a stable reference to the connection it runs on.
-- Default empty = no selection, the kind's own resolution rules, which is what every schedule
-- written before this column was created against. Forward compatible: the columns are only
-- added, and a binary that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN route TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN route_connection TEXT NOT NULL DEFAULT ''
