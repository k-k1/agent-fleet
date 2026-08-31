# 0030. Aborted turns — put self-healing on the notification seam, and auto-resume only the aborts a resend fixes

English | [日本語](0030-turn-abort-auto-resume.ja.md)

- Status: **adopted and implemented**. The design is [docs/47](../log/47-turn-abort-auto-resume.md).
- See also: [0015](0015-agent-managed-driver.md) (the managed driver's turn state machine) /
  docs/30 (completion reports and the operator) / docs/37 (the chat bridge)

## Context

When an API error cuts a turn short in a claude TUI session, **the Stop hook does not fire**. The
`working → idle` transition is recorded by nobody, and the pane simply returns to a waiting prompt.

What cleaned up afterwards was the self-healing that watches the pane and fixes the state cache
(`driveState` / `WireLive`), and all it did was call `status.Remove(sid)`. The results:

- No "answer ready" notification appears.
- The docs/30 completion report is never sent, and the report's arm is left unconsumed.
- In the Console it is simply "waiting for input" — the work stays stopped until a user notices.

Measured (session ssiw5kb, 2026-07-26 23:02:57 JST, `API Error: Connection closed mid-response.`):
zero notifications, with `session-report` left at `armed:true`.

`recordSessionNotification` already handled the case of "the marker has gone (previous == "")", but
that only helps *when the hook fires*. Here the hook never fired at all, so the safety net swung at
nothing.

## Decision

### 1. Self-healing stops deleting silently; terminal events go on the notification seam

When self-healing falls to idle, if the tail of the transcript is an API error it goes through
**`agents.MarkTurnEndErr`**. That is the seam built for the managed driver (docs/27), and it does
everything in one: write idle into status and call `recordSessionNotification`. Using it on the TUI
side too means **"which transition counts as completion" stays a single implementation** (we do not
give the TUI a second definition).

The other self-healing cases (kill+resume, a denied permission, an abandoned question) still delete
the marker silently. The key point is that **emitting a terminal event is restricted to real data in
the transcript**, so even if a tmux string heuristic misfires, no false "it completed" report is
produced.

### 2. Split aborts into "a resend fixes it" and "meaningless until the cause is fixed"

Alongside the existing `StateFailed` there is a new **`StateAborted`**. As an event they are the
same terminal (back to waiting for input; consuming the report for one dispatch), but the action to
prompt the operator with is the opposite:

| | Examples | The next move |
|---|---|---|
| `StateAborted` | a dropped connection, a temporary rate limit | **a resend fixes it** → auto-resume |
| `StateFailed` | a usage limit, no balance, prompt too long | **a resend does the same thing** until the cause is fixed |

The classification cannot be made from the status code alone. In the 16 cases measured across the
fleet, **both live under 429** ("Server is temporarily limiting requests **(not your usage limit)**"
and "You've reached your … limit"). So the judgement is **primarily on the wording, with the code
secondary**, and **anything undecidable falls to the blocked side** (not auto-resending is the safe
side).

### 3. The assistant (the operator) does the resuming; the Agent does not auto-resend

The instruction is put in the report body and the operator sends "carry on" with
`send_to_session`. The same shape as the question / plan autonomous running in docs/30, and for the
same reasons:

- From the user's point of view, **who sent what stays visible in the chat** (a back-channel send
  from the Agent would be invisible).
- Cases where **judgement about whether it is safe to resume is needed** — a crash part-way through
  a destructive operation, say — can be put on the human's or the LLM's guardrails.
- `send_to_session` carries `report_to`, so **the completion report after resuming is armed again** —
  the reporting cycle closes.

The price is that sessions not tied to a conversation (started from the Console) are excluded from
auto-resume. Notifications and the Console display appear as before, so there is no gap in
visibility.

### 4. The on/off switch is separate from autonomous running, and defaults to on

Autonomous running (answering questions and approving plans on the user's behalf) **substitutes for
the user's judgement**, so it defaults to off. Auto-resume **merely re-runs work the user already
asked for** and involves no new judgement. The dangerous aborts (limits, balance) are excluded by
the classification, and repeated resumes stop at `maxAutoResumeAttempts` (2). So it gets its own
toggle (`assistantAutoResume`), defaulting to on.

## Results

- An abort no longer "stops silently"; it rides the notifications, the reports and the chat bridge
  alike.
- An abort a resend fixes is resumed by the operator before the user notices.
- An abort that will not be fixed is not auto-resumed and comes up as a discussion of the cause and
  the remedy.
- Repeated aborts cut auto-resume off after two and escalate to the user.

## Addendum (2026-07-31) — the usage limit alone is resumed directly by the Agent

§3's "the Agent does not auto-resend" is a decision about **aborts tied to a conversation**, and
that does not change. The usage limit (`session limit` / `usage limit`) alone is an exception,
because:

- **It stops differently.** On hitting the limit, claude shows the `/rate-limit-options` menu and
  halts waiting for a keypress, **accepting no injection, no notification and no report** until a
  person dismisses it (docs/47 §4-3). Telling the operator to "send carry on" gets that send rejected
  with a 409. Something has to **recover the pane** first, and only the Agent can.
- **No judgement is required.** The only choice made is the default, "1. wait until it resets" — the
  option that costs nothing; asking for more quota (2) is never chosen. And "is it safe to resume?"
  can be treated like a dropped connection, because a limit is **always resolved by the clock**.
- **The wait is long.** Reset is hours away, and the workspace may stop meanwhile (the turn is
  over). Only the CP is alive while stopped, so the waiting is delegated to the scheduled execution
  in docs/38 (`spec_kind=once` / `session_mode=reuse`). An in-process timer cannot do it.

The price is giving up §3's first benefit (who sent what stays in the chat) for limit resumes. It
becomes a back-channel send from the Agent, so instead **it is left in the schedule list as a
one-off booking** (`spec_kind=once`, no repeat, deleted after use). The toggle is
`rateLimitAutoResume` (on by default, settings > agents > Claude > behaviour). It applies to every
claude TUI session regardless of whether there is an assistant conversation, so it moved from the
assistant settings to Claude's behaviour settings. Dismissing the menu happens even when the toggle
is off — without it the session can do nothing, and the person choosing has no billing decision to
make.

The state transitions are also left in the Agent's persistent notification outbox for the user. The
first detection of the limit menu is `rate-limit-reached`, and **after delivery of the resume prompt
is confirmed** it is `rate-limit-resumed`, each emitted to the notification centre exactly once per
episode / once-booking. The arrival of the booked time alone is not taken as a successful resume, so
an overlap, a vanished target or a delivery failure is never mis-notified as "resumed".

## Addendum (2026-08-05) — §3 inverted: the Agent resumes, and the assistant catches what it gives up on

§3 was "the assistant does the resuming; the Agent does not auto-resend". After the limit (the
previous addendum), **retryable aborts in general become an exception too**. The design is
[docs/47 §4-6](../log/47-turn-abort-auto-resume.md).

Two reasons.

- **Sessions with no conversation were never rescued.** This was stated as the price in §3, but in
  practice it became the majority (sessions started directly from the Console). Measured 2026-08-05:
  `API Error: Stream idle timeout - no chunks received` dropped 15 minutes' worth of a turn; the
  notification appeared, but nobody resumed it and it stayed stopped. Even with no gap in
  visibility, **the fact that it is stopped** remains.
- **A resume that needs no judgement was being routed through an LLM for judgement.** One abort
  report turn plus a `send_to_session` round trip ran on every abort. The content of the resume is
  always the same ("carry on"), and the operator adds no judgement.

The new shape: when the Agent finds a retryable abort it backs off and sends "carry on" itself (at
most `maxAutoResumeAttempts` = 2 times). **While it does, the abort report is not delivered** —
delivering it would mean the assistant's turn sending a request that has already been carried out.
If the aborts continue after two attempts it withdraws and reports with the existing escalation
wording (`reportKeyTurnAbortedCapped`). So what the assistant sees is only "aborts that did not fix
themselves".

How §3's three benefits are covered:

| §3's reason | How v2 handles it |
|---|---|
| who sent what stays in the chat | **Already solved.** With the injection-source recording in docs/37/38, a resume is badged in the mirror as `auto-resume` (a mechanism that did not exist when this decision was made) |
| put "is it safe to resume?" on a human or an LLM | Held down by restricting to retryable, two attempts, and a toggle (on by default). The side that does need judgement (limits, balance, prompt length, authentication) stays excluded by the classification |
| `report_to` comes with it, so the report is re-armed | No longer needed. The dispatch's row is **left open**, so the completion of the resumed turn becomes the completion report of that same dispatch (two reports become one) |

**The suppression is a delay, not a cover-up.** The abort notification still goes to the
notification centre as before. The suppression on the report side always has conditions that release
it (giving up, a TTL, the toggle off, no watcher) — a one-way ticket would be a different route back
to v1's "stops silently".

The resume prompt is a single phrase (`続けて（自動再開）`, "carry on (auto-resume)"). The abort was
tens of seconds ago and the context is intact, so it needs none of the explanation a limit resume
does (which arrives hours later).

## Left over

- It covers the claude TUI only. What it discriminates on is specific to claude's jsonl format
  (`isApiErrorMessage`), and the other TUI kinds (cursor / copilot / kiro) need different signals.
  Managed (codex / opencode) already has a reporting path through `StateFailed`.
- The resume prompt's language is the display language (`uiLocale`). A per-session language field
  would make it deterministic, but we do not have one yet.
