-- Which Control Plane process wrote a route_policy row. Only the operator ceiling row uses it:
-- the writer key (process start time, then a random id) orders two Control Planes that run at
-- once during a rollover, so the older one never overwrites the newer one's ceiling.
-- Empty for every other row and for rows written before this. Additive. Mirror: migrations-pg/0135_route_policy_writer.sql.
ALTER TABLE route_policy ADD COLUMN writer TEXT NOT NULL DEFAULT '';
