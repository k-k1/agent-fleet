-- Rule 1.5 on a second key: a stable claim the IdP hands out next to sub
-- (docs/61 §61.15.10 + ADR0043 決定 38).
--
-- 0041 keyed rule 1.5 on (realm, subject), which is exactly right for GitHub:
-- github.com hands the same numeric id to every OAuth App, so the same account
-- reached through two buttons is one subject. Entra is the opposite case. Its
-- sub is PAIRWISE -- a function of (application, user) -- so the same person,
-- through the head office app registration and through the subsidiary's, is two
-- different subjects on one issuer, and rule 1.5 never fires.
--
-- The naive repair is to store oid in identity_provider.subject instead. That
-- breaks the rows that already exist: the key changes, so rule 1 (the pair is
-- recorded) stops matching, and the login falls through. An env provider is
-- caught by rule 2 (the email joins it), but a TENANT-defined row has no rule 2
-- -- it lands on rule 2' and is refused as email_taken, which locks out the
-- people the feature exists for.
--
-- So subject keeps meaning sub, and the alternative key is stored NEXT to it:
--
--   realm_claim     which claim was read (oid, sub, ...) -- the NAME
--   realm_subject   the value that claim carried at this login
--
-- ★ The claim NAME is part of the match. One side reading oid and the other
-- reading some other claim must not join just because two unrelated values
-- happen to be equal, and the name is what says which question was asked.
-- Empty never matches, so every row written before this migration simply does
-- not take part -- and neither does a provider that names no claim.
--
-- ★ The value is never written from a tenant's row. The row names the CLAIM
-- (tenant_idp.link_claim, whitelisted to the claims we know are stable), and the
-- VALUE always comes out of the token the IdP just signed. A tenant that could
-- write the value could name anybody.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE identity_provider ADD COLUMN realm_claim TEXT NOT NULL DEFAULT '';
ALTER TABLE identity_provider ADD COLUMN realm_subject TEXT NOT NULL DEFAULT '';
ALTER TABLE tenant_idp ADD COLUMN link_claim TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_identity_provider_realm_claim
    ON identity_provider(realm, realm_claim, realm_subject)
