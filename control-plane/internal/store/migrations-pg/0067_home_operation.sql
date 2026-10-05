-- A home operation on ecs (#1544): a member's Recreate or Clean home, an administrator's
-- Clean home, Destroy or purge, each of which runs the stack's one-shot home task and then
-- has a step left to do in the Control Plane (the member's start, the audit outcome, the
-- workspace row's deletion). The row is written before RunTask and deleted, in the same
-- transaction as that step's database writes, once the task's outcome is known. A row
-- that outlives the process that wrote it is what the reconciler finishes.
--
-- id is the RunTask clientToken, so a RunTask asked again for the same operation answers
-- the task the first call started. task_sent_at is when that first call went out, written
-- before it: ECS keeps a token for at most 24 hours, after which asking again could start a
-- second task. phase is task until the task's outcome is known, then start while a member's
-- workspace is still to be started after a wipe that succeeded. One open operation per workspace (the UNIQUE), which is
-- also what refuses a Start while it is open.
--
-- No foreign key to workspace: Destroy deletes the workspace row in the transaction that
-- deletes this one, and a cascade from anything else would drop an operation whose task
-- may still be running.
CREATE TABLE IF NOT EXISTS home_operation (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL UNIQUE,
    membership_id TEXT NOT NULL,
    kind          TEXT NOT NULL,
    op            TEXT NOT NULL,
    phase         TEXT NOT NULL DEFAULT 'task',
    task_arn      TEXT NOT NULL DEFAULT '',
    task_sent_at  TEXT NOT NULL DEFAULT '',
    audit         TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);
