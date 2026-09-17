-- What a model is CALLED upstream, and the picture that shows what it draws (ADR 0088).
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`.
--
-- The sqlite counterpart is migrations/0068_engine_model_display.sql and the reasoning is there.
-- In short: the row id is derived from the file name because it is the key the launch menu and
-- the S3 layout are written in, so it is not a name anybody chose, and a catalogue of those is
-- unreadable. The publisher's name, its version's name and one example image all arrive on the
-- upstream read the ingest already makes. The two URLs are the card size and the lightbox size,
-- and nothing is mirrored into the bucket -- the row records where the publisher put the
-- picture and the browser fetches it.
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS display_name TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS version_name TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS preview_url TEXT NOT NULL DEFAULT '';
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS thumb_url TEXT NOT NULL DEFAULT '';
