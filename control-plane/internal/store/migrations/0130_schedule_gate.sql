-- ADR 0021 addendum (2026-11-10): an optional gate on a schedule, evaluated on each tick before
-- the fire. gate_kind is empty (no gate, every existing row) or work_items, and gate_spec is the
-- JSON the kind reads. The remaining columns are the gate's own ledger: the fingerprint it last
-- fired on, when, the 24 hour fire window (start and count) the daily cap reads, and the last
-- evaluation the Console shows. Forward compatible: the columns are only added, and a binary
-- that predates them ignores them.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE schedule ADD COLUMN gate_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_spec TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_fired_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_fired_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_window_start TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_window_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule ADD COLUMN gate_last_eval_at TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule ADD COLUMN gate_last_verdict TEXT NOT NULL DEFAULT ''
