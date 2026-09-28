-- The per-layer attention geometry, folded into the three numbers a KV-cache estimate needs.
--
-- 🔴 NEVER write a semicolon inside a comment in this directory. The runner splits a file on
-- semicolons, so one in prose cuts the next statement in half and the Control Plane stops
-- booting on `incomplete input`.
--
-- 0062 and 0069 describe a model whose layers all look alike. GPT-OSS, gemma-4 and LFM2 do not:
-- their layers mix full attention, sliding-window attention and short convolutions, with
-- different head counts and head widths per layer. So the header is read layer by layer and
-- summed into
--
--   kv_full_width     - sum of n_head_kv x (key_length + value_length) over the layers that
--                       cache every token of the window
--   kv_swa_width      - the same sum over the sliding-window layers, whose cache is capped
--   kv_sliding_window - <arch>.attention.sliding_window, which sets that cap
--
-- Measured 2026-09-28 against llama-server at -c 24576 with four slots: gemma-4-12b allocated
-- 384.00 MiB for 8 full layers and 1440.00 MiB for 40 sliding layers over 4608 cells, gpt-oss-20b
-- 576.00 + 24.00 MiB, LFM2.5-8B-A1B 288.00 MiB for its 6 attention layers - all as computed
-- from these three columns.
--
-- All three 0 means the row was read before they existed. The columns from 0062 and 0069 then
-- still answer, and the heal reads the header again.
ALTER TABLE engine_models ADD COLUMN kv_full_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN kv_swa_width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_models ADD COLUMN kv_sliding_window INTEGER NOT NULL DEFAULT 0;
