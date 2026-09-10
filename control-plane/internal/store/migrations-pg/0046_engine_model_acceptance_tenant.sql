-- The tenant axis on a licence acceptance (ADR 0072 open question 11, phase P5).
--
-- The catalogue itself stays deployment-wide: engine_models keeps its (role, id) primary key,
-- because a per-tenant catalogue multiplies the cold-start sync and a measured ~5 models fill a
-- ten-minute cold start on their own. What gets a tenant is the ACCEPTANCE, which is a human
-- act and belongs to whoever performed it.
--
-- Together with license_accepted_by / license_accepted_at (migration 0043) this makes the
-- tuple the ADR asks for -- (tenant_id, member_id, accepted_at, license).
--
-- ⚠️ No semicolon anywhere in a comment in this file. The migration runner splits the file on
-- semicolons with no SQL parser, so one inside a comment cuts a statement in half and the
-- Control Plane stops booting with "incomplete input".

-- Empty means the acceptance was the OPERATOR's -- a super_admin acts for the whole deployment
-- and has no tenant to be acting for. A tenant id here means a tenant_admin accepted it under
-- the grant their operator gave that tenant, and that is the row an audit of "who let this
-- model in" reads.
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS license_accepted_tenant TEXT NOT NULL DEFAULT '';
-- The licence AS ACCEPTED, in the words that were on screen at that moment. It is stored a
-- second time on purpose: license / license_name next door describe the model and are corrected
-- when the model card is, while this one is evidence about a past act and must not move when
-- upstream relicenses. A blank means nobody recorded one, not "no licence".
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS license_accepted_license TEXT NOT NULL DEFAULT '';
