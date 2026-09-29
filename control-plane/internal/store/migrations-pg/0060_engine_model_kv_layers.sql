-- The per-layer attention geometry, folded into the three numbers a KV-cache estimate needs.
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`.
--
-- The sqlite counterpart is migrations/0075_engine_model_kv_layers.sql and the reasoning is
-- there. In short: models whose layers mix full, sliding-window and convolution layers cannot be
-- described by 0047 and 0054's per-model numbers, so the header is summed per layer into a full
-- width, a sliding-window width and the window itself. All three 0 means the row predates them.
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_full_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_swa_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_sliding_window INTEGER NOT NULL DEFAULT 0;
