-- The two header fields that decide how many of a model's blocks actually hold a KV cache.
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`.
--
-- The sqlite counterpart is migrations/0069_engine_model_kv_hybrid.sql and the reasoning is
-- there. In short: 0047 stored block_count and multiplied by all of it, which is four times too
-- big for a hybrid model. `nextn_predict_layers` are blocks llama.cpp never runs, and
-- `full_attention_interval` says only every Nth layer caches per token — the rest are recurrent
-- and their state does not grow with the window. Measured on af-sandbox 2026-09-18, Qwen3.8-27B
-- at --ctx-size 262144 asked CUDA for `allocating 16384.00 MiB`, which is (65-1)/4 = 16 caching
-- layers exactly. All 65 would be 66560.
--
-- 0 means the architecture has no such field, or the row predates this column, and both reduce
-- to 0047's behaviour.
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_nextn INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN IF NOT EXISTS kv_full_attn_interval INTEGER NOT NULL DEFAULT 0;
