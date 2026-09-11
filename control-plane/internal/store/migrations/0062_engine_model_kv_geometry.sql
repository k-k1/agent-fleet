-- The attention geometry a KV-cache estimate is computed from (ADR 0074 open question 7).
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`. This has happened twice.
--
-- Until now the VRAM answer for a row was the sum of its files' bytes and nothing else, which
-- for the llm role misses the half that grows with the context window. Measured on the
-- deployment 2026-09-11: a 30B at 32768 tokens wants 3072 MiB of KV cache on top of 17524 MiB
-- of weights, so the weights alone said 4.8 GB of headroom on a 22.5 GB card where there was
-- 1.8. The four numbers below are what llama.cpp sizes that cache from, and they are read once
-- from the model's own GGUF header when the row is registered.
--
-- Four columns rather than one JSON blob, unlike files/args/sizes next door: these are scalars
-- that are read together and never as a list, and a zero in any of them has to be visible to
-- the query that fills them in later.
--
-- 0 means "not read" and is the default for every existing row. It is NOT a smaller estimate:
-- the caller keeps such a row at its floor, because a product with one factor missing is wrong
-- rather than conservative.
ALTER TABLE engine_models ADD COLUMN kv_layers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN kv_heads_kv INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN kv_key_len INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN kv_value_len INTEGER NOT NULL DEFAULT 0;
