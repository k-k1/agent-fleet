# 0105. A stop ends the running turn and moves on to the queue; a second stop ends the rest

English | [日本語](0105-stop-continues-into-the-queue.ja.md)

- Status: **accepted** (2026-09-30), **implemented** (2026-09-30): the Agent and the seven Managed drivers in #1308,
  the Console in #1309, the input origin in #1295. Not yet run on a live fleet. How it was built, the contract and the
  reviews are in [docs/log/128](../log/128-two-stage-stop-implementation.md). Decisions 1 and 4 were amended during the
  implementation (marked in place). The measurements are in [docs/log/127](../log/127-cli-stop-with-queued-input.md).
  The two-stage shape was the user's proposal in the #1258 discussion. The measurements then showed that claude
  and codex behave this way in the cases measured.
- Follow-ups: #1289 (implementation; open until a live-fleet run), #1256 (the CLIs not yet measured), #1255 (a queue
  that survives a restart), #1307 (a codex pump that outlives DropHandle)
- Supersedes: docs/log/27 §12.2-4 ("a stop reaches the queue"). Amends [0041](0041-cross-session-messaging.md)
  addendum 2026-09-30, decision 1 (only peer messages survive a stop).
- Related: docs/log/125 §4, docs/log/126 §4

## Context

The chat's **Stop the run (Esc)** means two different things today, depending on the execution method:

- **Terminal (CLI)**: it sends Esc to the CLI, and the CLI decides. Measured (docs/log/127, one run each):
  - claude 2.1.285 and codex 0.159.2 stopped the running turn and **sent the queued input at once**. claude put two
    queued inputs into one request, and codex says on screen "press esc to interrupt and send immediately". A
    second Esc stopped that request.
  - opencode 1.18.33 needs two Escapes. It did not run the queued input and kept it in the history, where it rode
    along with the next prompt.
- **Managed**: every driver's `Interrupt` stops the turn and **discards the member's own queued input**. Only
  another session's message (`KeepOnInterrupt`, ADR 0041) survives and starts as the next turn.

The Managed rule was written before peer messages existed, and it gets the most common case wrong. People queue a
follow-up mostly to correct the running turn ("no, do X instead"). Stopping the turn so that the correction takes
effect now throws the correction away. The rule is also inconsistent: a peer message continues and the member's own
input does not. A member who switches the same session between Terminal and Managed gets opposite results from the
same button.

Pulling in the other direction, a Managed session must always offer **one action that stops everything**, without
racing a timer. A follow-up that assumes the stopped turn succeeded ("then deploy it") must not go on with no way to
stop it first. On the Terminal route the queue lives inside the CLI, and Agent Fleet cannot promise this there
(decision 6).

The same `Interrupt` also backs things that are not labelled Stop. The Console's stop is also what rejecting a plan
and launching a plan review elsewhere send. On codex, cancelling or denying a question calls `Interrupt` too.

## Decision

### Decision 1: a first stop ends the running turn, and the queue continues

`Interrupt` stops the running turn and nothing else. What is queued, the member's own input and peer messages alike,
starts as the next turn once the stopped turn has settled. That is what the Managed drivers already do for peer
messages, and what claude and codex did in the measured runs.

- Queued inputs keep running **one per turn**, in order. claude put two queued inputs into one request, but that is
  not adopted here: a peer message's envelope and its `source: peer` in the transcript would blur into the member's
  text.
- `TurnInput` carries **its origin**, in the vocabulary of the mirror's injection badges (`recordInjection`): the
  member, a peer and its sending session, a spawn, the operator (`report_to`), a schedule, the chat bridge. This
  replaces `KeepOnInterrupt`, which only said "not the member". Today the origin never reaches the driver, so
  **every place that builds a `TurnInput` sets it**:
  - `/turn` start and steer (the Console's composer): member;
  - `/input`: peer, operator, schedule, or spawn, as `badgeOriginOf` decides for the badge today, and member
    when it decides nothing (an unmarked send);
  - `injectSessionPrompt` / `injectManagedPrompt`: shared by the chat bridge (bridge) and auto-resume
    (`abort_resume.go`, auto-resume), so it takes the origin as a parameter;
  - `create_session`'s initial prompt: along `noteCreateOrigin`'s branches, operator (`report_to`), schedule
    (manual runs included), spawn, or member (the ordinary Console launch and a handoff);
  - the resend of a carried answer (`sendManagedPrompt`): member.

  **Member input** in decisions 2 and 4 is the member and bridge origins: what a person typed as this session's
  user. Everything else, auto-resume and a manually run schedule included, is not.
- An input whose start is in flight **while no other turn runs** is not queued: it is the turn being stopped. A
  first stop stops it, as muse (`stopStarting`) and codex (`stopStart`) already do once the runtime names the turn.
  If the start fails, no turn was made and there is nothing to stop. The failed start is shown as it is today (codex
  adds a failed turn with the error). This includes input accepted while nothing runs that the pump has not taken
  yet. A first stop that stops such input before it reaches the runtime keeps its text for return like a discard
  (decision 4, reason `first_stop`): it is in neither the transcript nor the queue, and would otherwise vanish.
  (Amended 2026-09-30, implementation review.)
- Input sent while a turn runs is a **steer**. On codex and muse the runtime takes it into the running turn
  (native `turn/steer`), so it is not queued: it is part of that turn, and a stop ends it with the turn. The next
  turn still sees it in the conversation. The other drivers have no mid-turn injection and queue it, so there it
  continues after a first stop. Queueing a codex or muse steer instead would delay a correction the runtime could
  take at once; the difference is stated in the member guide. (Amended 2026-09-30, implementation review.)
- An input that waits **behind a running turn** is queued, wherever it waits: in the driver's queue, held by the
  pump (opencode, behind another client's turn, not yet sent), or in muse's host-side queue (sent). A first stop lets
  it continue; a second stop stops or discards it. Only input that is still cancellable (decision 3) can be
  removed or returned (decisions 4 and 5).

### Decision 2: a second stop ends the stop episode and discards the rest

A first stop that leaves anything queued opens a **stop episode**. The driver records the episode in a field of its
own, under the same lock that `accept` takes. It does not read the displayed turn state for this: `accept`
overwrites that state (codex, opencode and the ACP drivers set `queued`), so a send during `interrupting` would turn
a second stop into a first one.

- The episode lasts **until the queue is empty and the last turn it started has settled**, however many queued
  inputs that takes.
- It also ends when **new member input is accepted** (decision 1), whether it starts a turn or is queued. Input of
  any other origin does not end it. A refused send does not count, and neither does a resend. The resend check is
  made at accept time by looking the `ClientMessageID` up in the ledger without recording it, and against the ids
  already queued: copilot, cursor, kiro and lcpp record the ledger only when the pump takes the entry, so their
  `accept` alone cannot tell a resend from new input.
- **Any stop during the episode is a second stop.** It stops the running turn, or the input in flight, and
  **discards everything still queued**, peer messages included. It then ends the episode.
- A stop outside an episode is a first stop.

This is decided by state, **not by time**. The Console's poll, a phone's latency and a slow model all make "twice
within N seconds" unreliable. A member who notices only after the continued turn has run for a minute must still be
able to stop it.

The Console's stop control **stays usable while a stop request is pending**. Today `sendInterrupt` sets `sending`,
which disables the button until the first request answers, and that is exactly when a second stop is needed.

### Decision 3: one action stops everything

The stop control **always** carries a second action in its menu: **stop and discard the queue**. It is emphasised
while the Console sees something queued, but it is there even when the Console's view is stale, because the queue
on the server may already hold input the last poll did not show. It is the emergency brake the context asks for, and
it works without timing.

- It sends `/turn {"op":"interrupt","discard_queue":true}`. The driver discards all unsent input and stops the
  running turn under the lock that `accept` takes, whatever the episode state. A plain `interrupt` never does this.
- **Taken is not sent, and the line is drawn under the lock.** Every driver takes an entry out of its queue and
  sends it in two steps, releasing the lock in between (the ACP pump before `session/prompt`, muse's
  `beginStartLocked` before `turn/start`, opencode's release before `/message`). A taken entry is either
  **cancellable** or **committed**. The pump's last act under the lock, before it releases the lock to call the
  runtime, is to move the entry from cancellable to committed, unless a discard has cancelled it. A discard that
  takes the lock first cancels the entry, and it never starts. A discard that comes after the commit treats the
  entry as sent. Checking a mark and then sending outside the lock is not enough, because a discard can land
  between the two; that includes opencode's `abortAsked` check today.
- A committed entry moves on to **received** once the runtime holds it. The stop is delivered only then, never
  before: a cancel that overtook the input would find nothing and let it run. For muse and codex, the point is when
  the runtime names the turn (`turn/started`; the `turn/start` answer).
- The hand-over is decided under the lock, so exactly one side delivers the stop. A stop that finds the entry
  committed sets **stop-pending** and leaves the delivery to the pump. The pump, when it marks the entry received,
  delivers the stop itself if stop-pending is set (muse's `stopStarting`, codex's `stopStart`). A stop that finds
  the entry already received delivers the stop itself, at once. Neither side checks once and walks away.
- **ACP and opencode have no such point**, so there a committed entry's stop is best effort.
  - ACP: `session/prompt` answers only when the turn ends. Having written the request to the child's stdin says
    nothing about whether the runtime has registered the turn. A `session/cancel` written right after it may be
    handled first, find nothing, and let the prompt run. The drivers use the same hand-over with the write as the
    point, and send `session/cancel` then. The ordering of a cancel right after the write is unmeasured
    (docs/log/127 measured none of this), and until it is measured per ACP runtime the stop is not promised.
  - opencode: serve's `/session/status` reports a session busy or idle, with no id that ties it to this input, and
    `/message` blocks until the turn ends. So the pump cannot tell that serve holds this input rather than another
    client's turn. It could also miss a short turn entirely. An abort sent on a busy status can hit another
    client's turn, and the input can still run afterwards.
- **It is always reachable** in a Managed session that is running or has anything queued. That includes the time a
  question or approval card is shown, and it does not depend on the card's Cancel. Today the stop button is not
  rendered while a question is pending, and a codex question can be raised with input already queued behind it.
- **What the brake guarantees**: no input that was still uncommitted when the discard took the lock starts after
  it. Committed input is stopped as soon as the runtime holds it (above), so it can take its first step before the
  stop lands. That stop is guaranteed on muse and codex, and best effort on the ACP drivers and opencode. muse
  input queued on the host side behind a turn this driver did not start is outside the guarantee until
  `turn/unqueue` is measured.

### Decision 4: what is discarded comes back, from the driver

The driver keeps the entries a second stop (or decision 3, or a first stop under decision 1) discarded, per session and **per discard id**, until the
member restores or dismisses that discard. A later discard does not replace an earlier one. The driver keeps at most
the last 5 discards per session and drops the oldest beyond that. It exposes them next to `queuedPrompts` in the
session's messages payload. The
`/turn interrupt` response carries the same list, but the Console does not depend on it: a response lost to a closed
tab or a dropped connection is recovered by the next poll.

Each entry carries its text, its attachments and its origin (decision 1). Only input that was still cancellable
(decision 3) when it was discarded is here.

The Console shows a notice, for example "Stopped. 2 queued messages were discarded", with an action that puts the
discarded **member input** back into the input box, one message after another. It never sends. Discarded input of
other origins (peer, operator, schedule) is listed by origin in the same notice. It is not put back into the input
box, because the member did not write it. Restoring or dismissing tells the driver to drop that discard, so other
tabs stop offering it at their next poll. Two tabs can still each put the text into their own draft before that,
but nothing is sent twice: sending is the member's own act, and it gets a new `ClientMessageID`.

Limits: the kept entries live in the Agent's memory, so they are lost with an Agent restart, like the queue itself
(#1255), and a sixth discard drops the oldest. This decision gives no protection beyond that.

### Decision 5: the queue can be edited without stopping

The **Queued** bubbles in the chat get two actions: take the input back into the input box, and remove it. This is
the counterpart of claude's "Press up to edit queued messages", and the precise way to cancel one follow-up.

- Every queued entry has an id, and the id travels to the bubble; today `queuedPrompts` carries text only. The id is
  the entry's `ClientMessageID` where there is one, and a driver-minted id otherwise.
- A new `/turn` op removes an entry by id **while it is cancellable**: queued, or taken by the pump but not yet
  committed (decision 3). It takes the same lock and uses the same commit point, so removal and discard draw one
  line. It returns the entry, or `already_started` once the entry is committed. A stale bubble in another tab gets
  the same answer.
- The Console puts the text back into the input box only when the removal succeeded. Sending it again is a new send
  with a new `ClientMessageID`. The removed entry's own id is never reused, because drivers that record the ledger at
  accept time would drop the resend as a duplicate.
- Committed entries and entries already sent to the runtime (decision 1) are shown but have no actions.

### Decision 6: Terminal (CLI) sessions keep the CLI's own behaviour

On the Terminal route, Stop stays an Esc to the CLI, and a second press is a second Esc. In the measured runs, claude
and codex sent the queued input on the first Esc, and the second Esc stopped the request that carried it. Turn
boundaries inside the CLI were not observed. claude's rewind menu did not open while a request was running. Nothing
was left queued after the first Esc in those runs, so whether a second Esc also discards further queued input was
not observed. Agent Fleet does not add a queue or an episode of its own on this route, and decision 3's discard
action is not offered there: the queue lives inside the CLI, and Agent Fleet cannot empty it. opencode's Terminal
behaviour differs (its queued input stays in the history) and is left as the CLI's.

### Decision 7: which other actions follow the rule

- **Rejecting a plan** and **launching a plan review elsewhere** send the Console's stop. On Managed they follow
  decisions 1 and 2. On Terminal the stop is an Esc, and the CLI decides (decision 6).
- **Cancelling or denying a question** reaches `Interrupt` only on codex, so only codex follows decisions 1 and 2
  there. The other drivers answer the runtime's own rejection (opencode's question reject, the ACP permission's
  `cancelled` outcome, lcpp's answer channel). That answer does not touch the queue and is unchanged.

### Decision 8: teardown discards the queue

Halt, archive, recreate, switching the execution method (`DropHandle`), Agent shutdown (`AbortManaged`) and codex's
daemon drain discard the whole queue, as they do now. There is no runtime left to run it on. opencode's drain is the
exception today: it aborts the session without clearing the queue (docs/log/125 §3). Carrying the queue across that
restart instead is #1255's work.

### Out of scope

- A queue that survives an Agent restart (#1255).
- Notifying the sending session that its peer message was discarded. Decision 4 makes the discard visible on the
  receiving side only. docs/log/125 §4 rejected a sender notification for its protocol cost, and that still stands.
- Removing input that the runtime already holds. That covers muse's host-side queue (`turn/unqueue` is unmeasured)
  and codex's in-flight start. Decisions 2 and 3 stop such input once it becomes a turn; they do not return it.
- Whether a stop aborts a turn this driver did not start (opencode: an attached TUI's turn). docs/log/126 §4 records
  the current inconsistency.

## Rejected

- **Keep the current rule** (discard own input, continue peer). It discards exactly the correction the member
  queued, and it disagrees with the Terminal route and with both major CLIs.
- **Put own input back into the input box on the first stop** (the recommendation before the measurements). It keeps
  the first stop a full brake, but it costs an extra action in the common "apply my correction now" case. It also
  makes Managed disagree with the Terminal route in the other direction. It survives as the recovery path in
  decision 4.
- **Two permanent buttons** ("stop this turn" and "stop everything") side by side. They need space in a composer
  that already carries many controls, and they force a choice even when nothing is queued. Decision 3 puts the
  second action in the stop control's menu instead, always reachable and emphasised only when something is queued.
- **Show the discard action only while the Console sees a queue.** Rejected in decision 3: the Console's view lags
  the server by a poll, and the brake must not depend on it.
- **Press twice within N seconds.** Rejected in decision 2: it is unreliable over polling and mobile latency, and it
  cannot be expressed on the Terminal route anyway.
- **Merge the queue into one request, as claude did in the measured run.** Rejected in decision 1 for provenance.
- **Decide the second stop from the displayed turn state.** Rejected in decision 2: `accept` overwrites it.

## Consequences

- The plain first stop is no longer a full brake when something is queued. The full brake is decision 3's action,
  and decision 2's second stop, which needs no timing. Neither can take back input the runtime already holds, and
  the Terminal route has neither (decision 6). The stop button's hint should say that a second press ends what was
  continued. The wording is left to the implementation, taken from the Console's own strings.
- For claude and codex, the chat now behaves the same on Terminal and Managed, within what was measured.
- The `Interrupt` contract changes in all seven Managed drivers: `KeptOnInterrupt` goes, `TurnInput` gains its
  origin, and each driver gains the stop episode, the discard-queue interrupt, the kept discards, queue entry ids,
  the removal op, the cancellable/committed state of taken entries and a side-effect-free ledger lookup. Every
  `TurnInput` constructor sets the origin, and the messages payload gains the ids and the discards.
  The Console gains the stop control's menu action (kept reachable while a question or approval is pending), the
  notice and the bubble actions, and stops disabling Stop while a stop is pending. The member guide's chat
  chapter (07, en/ja), where the Stop button is described, states the two stops.
- The tests added in #1244 and #1258 that assert "own input is discarded by a stop" are inverted, not deleted: they
  become "own input continues after a first stop, and is discarded (and kept for return) by a second".
