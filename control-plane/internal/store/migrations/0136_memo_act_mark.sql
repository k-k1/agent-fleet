-- Message queues, issue 4218 (ADR 0137 section 6): the retire of a closed delivery's session is
-- conditional on what a person did after the report the close was decided on.
-- memo_delivery.act_mark is the Agent's actMark of the last kept report (the Agent-wide number its
-- session list carried), 0 for a report that carried none. The retire sends it as ifNoActSince, so
-- a session a person started or typed into after that read is left running.
-- Forward compatible: a column only, default 0. An older Control Plane never reads it and sends its
-- retire with no body, which is the unconditional retire.
ALTER TABLE memo_delivery ADD COLUMN act_mark INTEGER NOT NULL DEFAULT 0
