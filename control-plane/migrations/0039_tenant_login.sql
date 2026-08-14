-- Per-tenant login rules (docs/61 §61.9.7 + ADR0043 決定 15/16/19).
--
-- One deployment split into departments: each tenant declares which sign-in
-- buttons it accepts and which email domains join it automatically. All three are
-- CSV, NOT NULL DEFAULT '' — an existing deployment keeps exactly its current
-- behaviour until an administrator fills one in.
--
-- allowed_providers  which login providers may be used to enter THIS tenant.
--                    Empty = every provider the deployment enabled. Enforced at
--                    tenant resolution (resolver.go), not merely by hiding buttons
--                    on the login page - otherwise the generic /login plus a
--                    swapped X-AF-Tenant walks straight past it.
-- auto_join_domains  an address in one of these domains gets a membership of this
--                    tenant on first login. For small deployments that would
--                    rather not run invitations at all.
-- allowed_domains    a guard on the INVITE api only. It bounds who a tenant_admin
--                    may put on the roster, and is deliberately NOT a per-request
--                    constraint: making it one locks out the contractor who was
--                    invited on purpose, which then needs an exception list, which
--                    is the second roster this design exists to avoid.
--
-- ★ There is deliberately no allowed_emails column. The roster of "who may enter
-- this tenant" already exists and is membership - the invite API creates the
-- identity of someone who has never logged in. A second list would be a second
-- ledger of the same fact and the two would drift.
--
-- Forward compatible: an older CP binary selects the columns it knows and ignores
-- these, so a rollback of the binary alone leaves a working deployment.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE tenant ADD COLUMN allowed_providers TEXT NOT NULL DEFAULT '';

ALTER TABLE tenant ADD COLUMN auto_join_domains TEXT NOT NULL DEFAULT '';

ALTER TABLE tenant ADD COLUMN allowed_domains TEXT NOT NULL DEFAULT ''
