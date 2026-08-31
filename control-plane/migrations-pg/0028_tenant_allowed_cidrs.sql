-- Per-tenant source-network restriction (docs/log/66, ADR 0047). CSV of CIDR prefixes;
-- empty = no restriction, which is how the feature is switched off (there is no
-- operator on/off flag on purpose — ADR 0047 決定 5).
--
-- It sits next to allowed_providers rather than in the limits JSON because it is read
-- on EVERY request and the tenant login rules already have a 30s cache in front of
-- them, and because its owner is the tenant_admin, not the operator.
ALTER TABLE tenant ADD COLUMN allowed_cidrs TEXT NOT NULL DEFAULT ''
