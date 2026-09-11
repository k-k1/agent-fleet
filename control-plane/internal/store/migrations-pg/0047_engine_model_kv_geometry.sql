-- The attention geometry a KV-cache estimate is computed from (ADR 0074 open question 7).
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`. This has happened twice.
--
-- The sqlite counterpart is migrations/0062_engine_model_kv_geometry.sql and the reasoning is
-- there. In short: the VRAM answer used to be the files' bytes, which for the llm role misses
-- the half that grows with the context window — measured 2026-09-11, a 30B at 32768 tokens
-- wants 3072 MiB of KV cache on top of 17524 MiB of weights. 0 means "not read" and leaves the
-- row at its floor.
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_layers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_heads_kv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_key_len INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_value_len INTEGER NOT NULL DEFAULT 0;
