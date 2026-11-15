-- Message queues, issue 4218 (ADR 0137 section 6): the retire of a closed delivery's session is
-- conditional on what a person did after the report the close was decided on.
-- memo_delivery.act_mark is the Agent's actMark of the last kept report (the Agent-wide number its
-- session list carried), 0 for a report that carried none. The retire sends it as ifNoActSince, so
-- a session a person started or typed into after that read is left running.
-- memo_delivery.retire_act_mark is the mark one retire decision and its retries send, frozen when the
-- decision is made, so a later read cannot move the condition of a retire whose answer was lost.
-- memo_delivery.retire_gen is the generation (held_gen) of the report that mark was frozen from, 0 when
-- nothing is frozen, so that a frozen mark of 0 is told from none and a freeze is one conditional write.
-- Forward compatible: a column only, default 0. An older Control Plane never reads it and sends its
-- retire with no body, which is the unconditional retire.
ALTER TABLE memo_delivery ADD COLUMN act_mark INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN retire_act_mark INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN retire_gen INTEGER NOT NULL DEFAULT 0
