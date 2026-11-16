-- Message queues (ADR 0137 section 6): the act mark of 0136 comes with the identity of the run of the
-- Agent's counter that handed it out, so the Agent can tell an act recorded after a restart (or after
-- its state file was restored) from one recorded before the list the retire was decided on.
-- memo_delivery.act_incarnation is the Agent's actIncarnation of the last kept report, '' for a report
-- that carried none (an older Agent). memo_delivery.retire_act_incarnation is the identity frozen with
-- retire_act_mark, and travels with it.
-- Forward compatible: additive columns only, each default ''. An older Control Plane never reads them and
-- sends the mark without an identity, which the Agent reads as before, and a row written before the upgrade
-- holds '' and is sent as before.
ALTER TABLE memo_delivery ADD COLUMN act_incarnation TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN retire_act_incarnation TEXT NOT NULL DEFAULT ''
