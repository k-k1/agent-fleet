-- What a model is CALLED upstream, and the picture that shows what it draws (ADR 0088).
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`.
--
-- What it is for: the catalogue row's id is derived from the FILE name, because it is the key
-- the launch menu, the active set and the S3 layout are written in. It is therefore not a name
-- anybody chose -- `abyssorangemix2_hard_8832` is what the panel had to draw as the title of a
-- card, and a screen of those tells an operator nothing about which model is which. The name
-- the publisher gave it (`AbyssOrangeMix2`, version `Hard`) and one example image are what a
-- person recognises a checkpoint by, and both arrive on the SAME upstream read the ingest
-- already makes -- measured live 2026-09-18, `/api/v1/model-versions/<id>` answers
-- `model.name`, `name` and ten `images[]` in the one document the resolve decodes for the
-- licence and the sha256.
--
-- Two names because they are two facts: a model and the version of it that was taken in. The
-- pair is what tells two rows of the same model apart, which is exactly the case an id cannot
-- express either.
--
-- Two URLs because the card and the lightbox want different sizes, in Civitai's own path
-- vocabulary (`anim=false,width=256` and `width=1024`) -- the search tab has drawn the same
-- pair since it measured a page of originals at ~40 MB.
--
-- 🔴 URLs and not bytes. This deployment does not mirror the picture: the row records where the
-- publisher put it, the browser fetches it, and an image the publisher deletes leaves an empty
-- box that the metadata button re-reads. Copying it would put a third thing in the bucket that
-- purge, the ledger and the object routes would all have to learn about, for a thumbnail.
--
-- Empty is the default and means "nobody recorded one", which is every row taken in before this
-- existed and every row that came from the seed or from a plain URL.
ALTER TABLE engine_models ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN version_name TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN preview_url TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN thumb_url TEXT NOT NULL DEFAULT '';
