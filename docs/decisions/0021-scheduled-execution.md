# 0021. Scheduled execution: a resident scheduler on the CP wakes a stopped workspace and drives it, riding entirely on the existing create_session and docs/30

English | [日本語](0021-scheduled-execution.ja.md)

- Status: **in design** (2026-07-22). The implementation plan is [docs/38](../log/38-scheduled-execution.md).
- See also: [docs/30](../log/30-session-report.md) (completion reports — where results are delivered),
  docs/27 (the managed driver — the sessions being executed are one kind of it),
  the reaper (`control-plane/reaper.go` — it competes with this),
  the memo queue (`memo_bridge.go` — the precedent for an operation surface that queues on the CP and is drained by an agent),
  session_idempotency (`workspace/agent/session_idempotency.go` — absorbing double firing).

## Context

The operator (the `af_write` assistant) wants to set up "run this prompt every morning at 9"
tasks from a conversation. There is no foundation for it: the only periodic processing today is
a single deployment-wide singleton goroutine on the CP (the reaper and friends); there is no
mechanism for "wake and run" triggered by the clock, and no schedule-definition database.

The decisive constraint is that **while a workspace is stopped, the workspace-agent inside the
container is gone entirely**. Session execution, notifications and reports all live inside the
agent, so while stopped nobody can drive anything. What is alive while stopped is the CP and the
home disk, and **only the CP's `ensureWorkspaceStarted` can wake a workspace** (though today it
has no clock-triggered wake).

## Decision

1. **The scheduler is resident on the CP.** The CP, the only thing running while a workspace is
   stopped, takes on watching the clock, waking, and injecting. An in-workspace cron loop (the
   `fetch_loop.go` shape) only runs while the workspace is running and cannot satisfy "runs even
   while stopped", so it is not adopted.
2. **The default when the workspace is stopped is to wake it and run.** Truly scheduled
   execution is the default. Because of the resource and OOM risks, however, it **can be
   overridden per schedule to `skip` or `catch_up`**.
3. **Each firing goes into a brand new session** (`create_session`) by default. Reusing a
   long-lived session invites context bloat (depending on docs/33 compaction) and overlap, so it
   is left to a future extension.
4. **Execution and reporting ride entirely on existing assets.** Execution uses `create_session`
   (which already has report_to, an idempotency key and resume-after-stop), and reporting uses the
   report seam from docs/30 (`recordSessionNotification` → `/chat/report` → role="report" plus a
   notification plus an automatic turn) as is. No execution or reporting machinery is built
   specifically for scheduled runs.
5. **The operation surface has the same shape as the memo queue.** The operator MCP
   (`create_schedule` and friends, gated on `af_write`) → the Agent → CP internal (an internal
   token route modelled on `cpMemoDo`) → the CP database. The canonical definition lives in the CP
   database (only the CP can read anything while stopped, so a `~/.config` proposal is not
   adopted).
6. **Coordinate explicitly with the reaper.** A firing has no open Console connection, and left
   alone the reaper would stop the workspace immediately after injection, killing the session and
   the report. The scheduler registers a keep-alive with the reaper while running and keeps the
   workspace out of reclamation until "the target session reaches idle and the report is
   delivered". **A workspace that we woke (i.e. was stopped) goes back to stopped after a settle
   grace period** (so that follow-on automatic turns, or a user action right afterwards, are not
   dropped). A workspace that was already running is never stopped on account of a schedule.
7. **The spec may be entered in natural language.** The operator translates it into a structured
   spec (cron/interval/once plus tz) at registration time and confirms its interpretation and the
   next firing with the user. The canonical form in the database is the structured spec; the
   natural language stays as a `spec_label` for display (to avoid non-determinism and silent
   failure from parsing at execution time).
8. **`run_now` (manual firing) is in v1.** It goes through exactly the same path as a scheduled
   firing (wake policy, idempotency, keep-alive), so a manual run does not slip past the
   safeguards.
9. **Unattended failures are not silent.** When an expired credential, exhausted usage, or a
   failed wake/injection is detected, an error kind is added to the report seam and reported to the
   operator. Where possible, check the rate limit before firing and skip with a "skipped" report.
10. **Template variables in the prompt are fixed metadata only.** Only `{{date}}` / `{{time}}` /
    `{{datetime}}` / `{{tz}}` / `{{schedule_id}}` / `{{schedule_label}}` / `{{last_run}}` —
    non-data values the scheduler computes deterministically at firing time — are allowed. "Data"
    such as report bodies, the previous session's output, or git/workspace state is not carried
    (so as not to open an injection surface where attacker-influenced data flows into an
    unattended run). Expansion happens on the scheduler side at firing time, and an undefined
    `{{foo}}` passes through literally. Extending the whitelist may be considered in future, as
    long as it does not cross the line of "non-data values fixed at registration or by the
    scheduler".

## Results (expected, and the constraints accepted)

- Scheduled tasks can be set up from the operator conversation; a stopped workspace wakes when
  the time comes and runs, and the result is reported back into the conversation automatically
  (the automatic turn carries it on into follow-up processing).
- New implementation is limited to four things: the CP scheduler goroutine, the schedule
  database, an internal authentication route for a clock-triggered wake (generating
  membership→resolved internally), and the operation MCP. Execution and reporting already exist.
- Constraints accepted:
  - **Waking competes head-on with scale-to-zero.** A thundering herd at a popular time is a host
    OOM risk (with real damage on record). Mitigated by jitter, a cap on concurrent wakes and
    respecting the `max_workspaces` quota — and even so, the trade-off of "punctuality chosen over
    frugality" remains.
  - **The security surface of a powerful primitive**: since it can be set up to "wake a stopped
    workspace and run an agent unattended", the persona explicitly requires user confirmation
    before registering anything on the basis of a report body (attacker-influenced data) — an
    extension of the docs/30 guard.
  - **Firings across a CP restart** are handled by persisting `next_run`/`last_run` in the
    database and a deterministic idempotency key of (schedule_id + firing slot), which absorbs
    both double firing and missed firing (an in-memory ledger is not enough).
  - v1 goes as far as injecting one prompt on cron/interval/once. Workflow DAGs, GUI creation,
    long-lived reuse and sub-minute frequencies are out of scope (later phases in docs/38).
