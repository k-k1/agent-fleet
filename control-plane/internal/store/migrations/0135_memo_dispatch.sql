-- Message queues, step 3 (ADR 0137 sections 6 and 7, issue 4204): fires that start by themselves.
-- Settings, written by the Console's PUT: open_cron, open_minutes and tz make a window (the queue
-- is open for open_minutes after each time the cron matches, in the IANA zone tz, UTC when
-- empty), open_after makes a threshold (open once that many messages are pending, quiet for a
-- minute, then draining until none is), and with none of them the queue is always open.
-- max_fires_per_day bounds the deliveries created in the last 24 hours.
-- State, never written by that PUT: draining (a threshold queue that is emptying), paused,
-- paused_at and paused_reason (the circuit breaker), breaker_after (the breaker counts only
-- deliveries created after it, set when a person resumes), no_report_at (when a fire by itself
-- found the workspace's Agent not sending the queue report).
-- memo_delivery.auto is 1 for a delivery the dispatcher fired by itself, which checks after the wake
-- that the Agent sends the queue report.
-- memo_delivery.limit_kinds is the agent kinds of the sessions of the tree that waited for a usage
-- limit at the last kept report, as a comma list with a comma at each end.
-- memo_delivery.drain is 1 for a delivery claimed while its queue was draining, so that its
-- messages returning as not sent put the queue back to draining.
-- memo_delivery.turn_tried_at is when a closed delivery was last read for the first turn of its
-- session, so that the deliveries never tried come first and none is read forever ahead of the rest.
-- Forward compatible: columns are only added, with defaults that mean today's behaviour (always
-- open, 20 a day, not paused). An older Control Plane never fires by itself, its PUT leaves
-- every one of these columns as it is, and it cannot lift a pause.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
ALTER TABLE memo_queue ADD COLUMN open_cron TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN open_minutes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN tz TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN open_after INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN max_fires_per_day INTEGER NOT NULL DEFAULT 20;
ALTER TABLE memo_queue ADD COLUMN draining INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN paused INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_queue ADD COLUMN paused_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN paused_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN breaker_after TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_queue ADD COLUMN no_report_at TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN auto INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN limit_kinds TEXT NOT NULL DEFAULT '';
ALTER TABLE memo_delivery ADD COLUMN drain INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memo_delivery ADD COLUMN turn_tried_at TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_memo_delivery_turn ON memo_delivery(queue_id, created_at) WHERE turn_ended_at <> ''
