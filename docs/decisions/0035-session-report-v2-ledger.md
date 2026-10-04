# 0035. Session reports v2 — drop edge-driven firing and the 1-bit arm for a dispatch ledger and a level-driven reconciler

English | [日本語](0035-session-report-v2-ledger.ja.md)

- Status: **adopted and implemented** (decided 2026-07-28; on 2026-07-29 Phase 1 "unify the
  judgement", Phase 2 "replace with the ledger" and Phase 3 "compensating reopen plus a self-report
  fast path" were implemented). The design proper is
  [51-session-report-v2-ledger.md](../log/51-session-report-v2-ledger.md).
- See also: [docs/30](../log/30-session-report.md) (v1's design and its history of incidents — it has no ADR of its own) /
  [0030](0030-turn-abort-auto-resume.md) (abort classification — absorbed into v2's predicates) /
  [0015](0015-agent-managed-driver.md) (the notify seam — demoted to a hint in v2)

## Context

v1's reporting machinery (docs/30) is structured as "catch an edge such as the Stop hook once, and
consume an irreversible 1 bit (the arm) to report". Patch upon patch was applied — saga5uc (an early
Stop consumed right after a BG launch) → a pending waiter was added; sqmconc (the waiter consumed
early in the window of a false idle heal) → three delivery conditions were added — but every new
seam in the concurrency creates a new window for "mechanical idle ≠ semantic completion". The known
holes that remain (a dispatch crushed when queued, a generation race on consumption, TUI polling
kinds being defenceless, delivery lost by consume-then-deliver, a kick lost while the agent
restarts) all reduce to one of **identity (1 bit), detection (a one-shot edge inference) or delivery
(irreversible consumption)**.

## Decision

1. **Make a dispatch's identity a row in a ledger.** The 1-bit arm is abolished: one dispatch = one
   row (id, conv, time injected, a progress cursor, and a state machine of
   pending/interim_reported/reported/reopened/cancelled). Overlapping dispatches are no longer
   "crushed" — they are **explicitly folded into one message at settle time**.
2. **Make detection level-based (state convergence) rather than edge-based.** A single reconciler
   inside the server re-evaluates pending rows on a tick and on hint wake-ups. Settle is "idle
   evidence ≥ 1 ∧ busy evidence = 0 for two consecutive ticks" — the default of "no marker = idle" is
   abolished and unknown is treated as unknown. A false "not yet" self-corrects on the next tick.
   Hooks, the notify seam and record-exit are demoted to wake-up hints, so a miss degrades into a
   delay rather than a loss.
3. **Make delivery idempotent on the sink side.** Deduplicate by row id under the conversation lock,
   and advance the ledger only when the append succeeded. This removes the "exactly once"
   responsibility from the detection side.
4. **Make a false "completed" recoverable by compensation.** A reported row is watched for a grace
   period, and a return to busy with no new dispatch produces a correction report (the report role —
   not a notice, as §Impact says) plus a reopen (up to twice). This breaks the asymmetry of "consumed
   in error = unrecoverable".
5. **Self-reporting stays a fast path.** The `af_report` MCP tool (Phase 3) is one piece of idle
   evidence plus a wake-up hint, not the backbone. It is not made stronger than busy evidence
   (calling it early leaves the row pending). The report body remains server-generated facts only.
6. **Migrate in stages.** Phase 1 = unify the judgement (keeping the arm bit; remove the waiter and
   the pending special cases), Phase 2 = replace with the ledger, Phase 3 = compensation plus
   self-reporting. Each phase can be rolled back independently. The external contracts — the report
   body, interim, the automatic turn and the disarm convention — are unchanged.

## Options rejected

- **Carry on hardening edge + arm incrementally**: the sqmconc fix (three conditions) narrowed the
  window, but every new seam calls for another patch of the same kind. It never ends as long as a
  false consumption is structurally unrecoverable.
- **Make self-reporting the backbone**: it is the only way to measure semantic completion directly,
  but it stakes the whole correctness on the model remembering to call it and not calling it early,
  and gives no certainty across kinds. Adopted as a fast path only.
- **Report on every Stop and let the operator (LLM) judge duplicates**: it pushes correctness onto
  model judgement and increases report spam and automatic-turn consumption. It also breaks the "one
  dispatch = one report" contract.
- **Periodic polling from the operator conversation (running get_session_status on automatic
  turns)**: it consumes LLM turns permanently. The judgement should be made cheaply, by machine.
- **Include the process tree (BackgroundBusy/BackgroundShellBusy) as busy evidence**: it cannot be
  distinguished from a resident dev server or a watch loop, and would leave rows pending forever —
  v1's reason for accepting this is maintained.

## Impact

- The detection logic is collected in one place (the reconciler plus an evidence table), and the
  acceptance criteria for a new kind are made explicit as "fill in the table". TUI string drift
  becomes a delay rather than a lost report.
- Latency when a hint is lost is +1–2 ticks (~60s). Tests pin that this is no worse than v1's 90s
  waiter wait.
- `session-report/*.json`, the waiter and the generation-arbitration code are removed at the end of
  Phase 2. (As implemented on 2026-07-29: the arm store became a leftover that only the startup
  migration `migrateReportArms` reads, and `consumeReportArm` / `reportArmMu` are gone. A dispatch's
  identity is carried by the row id in `instr-ledger/<session>.json`.)
- A false "completed" degrades, under a 10-minute grace watch, into a **correction report** with
  `kind=reopened` (the report role, not a notice — a notice is not replayed into the operator's
  context) plus reopening the row. The correction's idempotency key is in a different namespace from
  the completion report, and "which report" the correction refers to is taken from the conversation
  message rather than the ledger (`reported_at` is cleared by a reopen).
- Self-reporting is distributed to sessions of every kind that has a CLI, via the built-in MCP server
  `af` (`workspace-agent mcp-stdio --self-report`), injected as one line into the instruction prompt.
  The receiving end is the existing `POST /chat/report` (`kind=self-report`); no new delivery path or
  persistence was added.
- After the Chromium Attach View correction on 2026-08-02, the current builtin starts as
  `workspace-agent mcp-stdio --self-report --chromium-attach` and advertises, in addition to
  `af_report`, only the seven Chromium tools to interactive sessions. `af_report`'s meaning and
  receiving end are unchanged, and the decision that "the advertised set is the scope boundary", so
  that other fleet tools cannot be called by guesswork, is maintained. `--self-report` on its own
  still advertises exactly one tool.

## Addendum (2026-10-03) — an instruction whose prompt never ran

On a Managed session an instruction's prompt can wait in the session's queue behind a running turn, survive a halt,
and be dropped before it runs (#1257, [0105](0105-stop-continues-into-the-queue.md) addendum 2026-10-03). The row is
raised before the send, marked `sending`, and the prompt carries the row id (`TurnInput.Instr`, kept in its held
file), not a message id: a driver may rewrite the id it is given (opencode does). The send's outcome clears
`sending`, or withdraws the row when the driver refused the prompt.

- While the row is `sending` or its prompt waits (`agents.HeldInstrs`), the row is left out of the settle decision:
  the turn it queued behind ending, or being stopped, is not its completion, and a report delivered for a prompt
  the driver then refuses could not be taken back.
- `sending` holds the boot id of the Agent process sending it, so a row left `sending` by an Agent that is gone
  (another boot id) is told apart from this process's own send however a sweep interleaves with it. Such a row
  is settled by evidence, not by time. With a held file the driver had accepted the prompt, and the row becomes an ordinary queued row. Without
  one nothing shows whether the prompt reached the session, so it gets an `unconfirmed` report and is closed as
  `unconfirmed`: neither a completion nor a not-run is asserted, and the operator is told to look before resending.
- When the prompt is dropped, the row records why (`dropped`), and the next sweep delivers a `not-run` report with
  that reason, without waiting for quiet evidence and even when the session's meta is gone. Delivered, the row is
  closed as `not_run`, which is never a reopen candidate. A retry keeps the row open, as for every report.
- The operator's `stop_session` cancels the rows first, so the prompts it withdraws are not reported back to it.

## Addendum (2026-10-04) — a Stop another hook blocked

claude and codex run all of a Stop event's hooks in parallel, so ours writes the end-of-turn marker before it can know
that a user's Stop hook answered `decision:"block"` and the turn goes on (#1600; measured on claude 2.1.288, read in
codex's source). When the continued turn stays quiet past the transcript's freshness window and the pane is not read
as busy, two sweeps deliver the report at the blocked stop. Compensation cannot take it back, because an idle marker
reads as "the turn ended", so the real end is never reported (reproduced in the reconciler test).

- A marker whose stop was blocked is busy evidence (`stop-continued`), read off what the CLI records rather than
  off the hook. claude: the newest `stop_hook_summary` in the transcript tail is preceded by a Stop
  `hook_blocking_error`, with no interruption after it. codex (Terminal): the rollout's newest lifecycle event is
  an unended `task_started`, since codex writes `task_complete` only after the last Stop.
- When that evidence is missing or unclear, the old behaviour applies. An abort at the transcript tail also ends
  the continued turn. Managed codex ends on `turn/completed` and the other kinds have no Stop hook, so they are
  unchanged.
