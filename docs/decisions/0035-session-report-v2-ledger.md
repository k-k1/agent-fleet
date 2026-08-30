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
