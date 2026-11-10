-- ADR 0021 addendum (2026-11-10), Postgres mirror of migrations/0130_schedule_gate.sql. See that file.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_fired_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_fired_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_window_start TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_window_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_last_eval_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN IF NOT EXISTS gate_last_verdict TEXT NOT NULL DEFAULT ''
