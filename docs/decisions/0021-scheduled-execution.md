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

## Addendum (2026-10-03) — delivery targets and the silent sentinel

A schedule chooses where its result goes, and may stay quiet when there is nothing to say (#1560). Decision 4
stands: nothing here is a delivery path of its own. The run's row in the report ledger
([0035](0035-session-report-v2-ledger.md)) carries the schedule's choice, the reconciler settles it like any row,
and only the sink routes it differently. Decision 10 is untouched: the prompt's template variables are unchanged.

- **Targets** (`deliver_to`, with the report on): any of the operator conversation (the default, and what
  `report=true` meant before), the notification center, and the member's own Discord / Slack connection, reusing
  the bridge's send path ([0020](0020-chat-bridge.md)). No new integration and no free-form webhook. A schedule
  that asks for nothing beyond the operator conversation sends the Agent exactly the body it sent before.
- A chat post goes only through a connection that is connected, not muted, and **bound to its member** (0020
  decision 5's identity binding). The connection lives in the member's workspace, so the Agent checks it at
  delivery time, not the Control Plane at registration. A connection that cannot take the post is named in a
  notification instead of being skipped. The post carries the answer whether or not the connection is in
  full-text mode: naming the connection for this schedule is the explicit opt-in that mode exists to ask for.
- A run that names its targets reaches **only** them. The broadcast copies every chat connection otherwise gets
  (the turn's answer-ready, the operator report's notification) are not raised or not bridged for it. A chat
  post is queued once per run, report kind and connection, across restarts, and a queue that cannot be written
  never holds back the notification center.
- **Each run's answer is its own, read from the session's transcript** through the kind's agent, Terminal and
  Managed alike (a Managed driver hands its turn end over with no text). The run is matched to its own prompt
  (the delivered text, oldest run first, each prompt claimed once), so two open runs of a reuse session never
  share an answer, and a later `[SILENT]` cannot hide an earlier alert. The sink waits up to two minutes for the
  answer to appear; past that it delivers without a body, never as silent.
- **Each run's outcome is its own too.** A turn end records clean / failed / aborted for the run it ended. A
  Managed driver names that run by the input it started the turn with (the instruction row id); only a turn
  started without one in hand (a Terminal session's hook, a Managed turn taken over across a restart) falls back
  to the run whose prompt precedes the instant the end was seen. The outcome is kept until the ledger save that
  consumes the row has landed, and
  the sink trusts that over the reason the reconciler settles with (a later run's end can settle an earlier
  run). A failure is never washed out by a later clean end, and a run is silent only on its own recorded clean
  end. The verdict a turn end called for (routed / silent) is kept by its completion key, so a hook that runs
  after the rows were consumed still does not broadcast.
- **Silent sentinel** (`silent`): a run whose final answer, trimmed, is exactly `[SILENT]` (case-sensitive)
  delivers nothing — no report, no notification, no chat post — and the Agent asks the Control Plane to record
  the run as `fired_silent`, which the history shows. Only schedules that enable it tell the agent about it, in a
  note ahead of the prompt. A wrong match would hide an alert and a wrong miss costs one message, so the match is
  the whole answer, never a substring.
- **Failures are never silent**: a failed or cut-off turn, an abnormal exit, and a run reported unconfirmed reach
  the notification center whatever the targets say (a run dropped before it ran is already notified by the
  Control Plane).
- Out of scope: runs in `session_mode=assistant` (the run is itself a turn in the conversation, so the Control
  Plane refuses targets and the sentinel there), script-only runs, and chaining one run's output into the next.

## Addendum (2026-11-10) — gated schedules, slice 1 (#1763)

A fire used to be clock-only: every tick starts a session and spends a turn whether or not there is anything to do
(the silent sentinel above hides the answer, not the cost). A schedule may now carry a **gate**, a cheap check the
scheduler runs first. The existing new-session path fires only when the check says so **and** its result differs
from the one the gate last fired on. Decisions 3, 4 and 10 stand; decision 10 is amended in one place, below.
Slice 1 is the declarative gate only. Later slices (probe wakes, a workspace-side script gate, an inbound trigger)
stay in #1763.

- **Gate kind `work_items`**: one of the member's saved work-item queries ([0061](0061-work-item-inbox.md)),
  resolved by the running Agent through the existing `POST /work-items/fetch`. No code runs, no new credential,
  no new route and no inbound endpoint. The query must belong to the schedule's owner, at save and again at every
  evaluation. A gate applies to recurring `session_mode=new` schedules only (reuse and assistant fires have no
  per-slot idempotency, and a `once` has nothing to poll).
- **What "differs" hashes**: the sorted, de-duplicated item keys (`fingerprint=keys`), or key plus `updated_at`
  (`keys+updated`, which also fires on an edit). Titles and bodies never enter it. It is compared with the
  fingerprint of the last *fired* result, not the last seen one, so a red that flaps red/green/red does not
  re-fire. An empty result is `clear`.
- **Claim, then inject**: one conditional write (on `next_run` and the stored fingerprint) advances the ledger and
  records the fingerprint, and only then is the session injected. Of two ticks that listed the same row exactly
  one wins. A crash between claim and inject loses that one fire (at-most-once), deliberately: a missed triage
  leaves the red red for the next change or a run-now, while a duplicate burns quota and files duplicate issues.
  A failed inject is final for the same reason, with the usual error notification. The claim stores the verdict
  `claimed` and recording the fire turns it into `fire`, so a tick that still finds `claimed` knows a crash lost
  the previous fire and raises one `error:gate:not_delivered` notice (the fire itself is not redone).
- **Fail closed**: an error from the check (the query gone, the tracker rejecting it, an Agent without the route, an
  unreadable spec) never fires. It is stored on the row (`gate.last_verdict = error:…`) and, once per streak of
  consecutive errors, written to the run history and the notification center. An Agent that does not answer, and a
  workspace that is not running, are silent skips: slice 1 never wakes a workspace to check.
- **Skips leave no run row**: `clear`, `unchanged`, `not_running`, `unreachable` and a hit cap only update
  `gate.last_eval_at` / `gate.last_verdict`, because at a 2 hour cadence the 50-row history would otherwise
  hold four days of nothing. A fire is an ordinary fire in every other respect (`deliver_to`, the sentinel,
  the report ledger are unchanged).
- **Caps**: `max_fires_per_day` (default 2, in a rolling 24 hour window opened by the first fire) and
  `min_fire_gap_s` (default 21600), checked only for a result that differs from the last fire.
- **A run-now bypasses the gate**: it is the member's own press, so it fires whatever the gate would say, and carries
  no `{{gate.refs}}`. It does not touch the gate ledger, so the next tick may fire the same red once more.
  Changing the gate's query or fingerprint mode, or removing the gate, clears the ledger.
- **Amendment to decision 10**: a gated schedule's prompt may use `{{gate.refs}}`, the matched item keys, at most
  20, sorted. Each key must match `owner/repo#N` or `PROJ-123`; any other key is dropped. This is the only
  variable that carries data, and the data is structured identifiers, never text a third party wrote. On an
  ungated schedule the token stays literal.
- **Data model**: additive columns on `schedule` (`gate_kind`, `gate_spec` JSON, and the ledger: fired fingerprint,
  fired-at, window start and count, last evaluation and verdict). The API carries them as `gate` on the schedule.
- **Not in this slice** (#1763 follow-ups): the Console form and display, the `create_schedule` / `update_schedule`
  arguments and persona text, a notice when a cap holds a changed result back, backoff of a failing gate,
  `stop_after_run` defaulting to on for a gated schedule, `refire_after`. Follow-ups: #3809.

**Note (2026-11-11, #3809):** the `create_schedule` / `update_schedule` `gate` argument and the operator persona text had
already landed. The Console now has the gate section in the schedule detail modal (query picker over the member's saved
work-item queries, fingerprint mode, the two caps, and a read-only "last checked" line from `gate.last_eval_at` /
`gate.last_verdict`). The modal sends `gate` only when it was edited, and then always whole, because the CP rebuilds the
stored spec from it. Still open: the cap-held-back notice, gate backoff, `stop_after_run` default, `refire_after`, and the
live run (#3809).

**Note (2026-11-14, ADR 0137):** [ADR 0137](0137-message-queues.md) amends decision 10, and the 2026-10-03 addendum's
exclusion of "chaining one run's output into the next", for queue fires only. A queue fire's prompt is the queue's
standing instruction, which a person registered, followed by the queued messages marked as data; each was written or
released by the member and names its origin (0137 §5). A tidy step may rewrite those messages before the fire, by a
call that has no tools and whose reply is validated (0137 §8). Schedules are unchanged: a schedule's prompt still
carries only the fixed variables and `{{gate.refs}}`. Nothing of 0137 is built yet (#4148).
