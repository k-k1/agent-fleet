-- Message queues, the last answer of a delivery's root session (ADR 0137, issue #4876). When a
-- delivery closes, the dispatcher reads the first line of that session's last answer from the
-- running Agent and keeps it here, so a run that opened no PR satisfies a dependent's `after`
-- only when it ended with an answer that starts with Done:.
-- answer_head is that first line (at most 200 characters), answer_class how it reads: done,
-- partial (Partial:, Held:, Deferred:, Too large:, Stopped at part), other, or unknown (the
-- session or its transcript could not be read). Both are empty until a read decided them, and
-- written once. answer_tried_at is the last read begun.
-- Forward compatible: columns with defaults. A binary that predates them ignores them and lets
-- a run without a PR satisfy its dependents as it did.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_delivery ADD COLUMN answer_head TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN answer_class TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN answer_tried_at TEXT NOT NULL DEFAULT ''
