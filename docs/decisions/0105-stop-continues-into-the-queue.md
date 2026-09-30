# 0105. A stop ends the running turn and moves on to the queue; a second stop ends the rest

English | [日本語](0105-stop-continues-into-the-queue.ja.md)

- Status: **proposed** (2026-09-30). The measurements are in [docs/log/127](../log/127-cli-stop-with-queued-input.md).
  The two-stage shape was the user's proposal in the #1258 discussion. The measurements then showed that claude
  and codex behave this way in the cases measured.
- Follow-ups: #1282 (this decision), #1256 (the CLIs not yet measured), #1255 (a queue that survives a restart)
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

Pulling in the other direction, the member must always have **one action that stops everything**, without racing a
timer. A follow-up that assumes the stopped turn succeeded ("then deploy it") must not go on with no way to stop it
first.

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
- `TurnInput` carries **where it came from**: the member, or a peer and the sending session's name. This replaces
  `KeepOnInterrupt`, which only said "not the member". Today `peer_from` stops at `/input` and never reaches the
  driver, so decisions 2 and 3 need the field.
- Some input has left the queue but is not yet a turn (docs/log/126): opencode's input held behind another client's
  turn has **not** been sent, while muse's in-flight `turn/start` and codex before the `turn/start` answer **have**.
  All of them count as queued for decisions 1 and 2. A first stop lets them continue; a second stop stops them.
  Only input that has not been sent can be removed or returned (decisions 3 and 4). Stopping a sent input is a stop
  of its turn, which the transcript already shows as interrupted.

### Decision 2: a second stop ends the stop episode and discards the rest

A first stop that leaves anything queued opens a **stop episode**. The driver records the episode in a field of its
own, under the same lock that `accept` takes. It does not read the displayed turn state for this: `accept`
overwrites that state (codex, opencode and the ACP drivers set `queued`), so a send during `interrupting` would turn
a second stop into a first one.

- The episode lasts **until the queue is empty and the last turn it started has settled**, however many queued
  inputs that takes.
- It also ends when **the member's new input is accepted**, whether the input starts a turn or is queued. A ledger
  duplicate or a refused send does not count. Peer input arriving does not end it.
- **Any stop during the episode is a second stop.** It stops the running turn, or the input in flight, and
  **discards everything still queued**, peer messages included. It then ends the episode.
- A stop outside an episode is a first stop.

This is decided by state, **not by time**. The Console's poll, a phone's latency and a slow model all make "twice
within N seconds" unreliable. A member who notices only after the continued turn has run for a minute must still be
able to stop it.

The Console's stop control **stays usable while a stop request is pending**. Today `sendInterrupt` sets `sending`,
which disables the button until the first request answers, and that is exactly when a second stop is needed.

### Decision 3: one action stops everything

While anything is queued, the stop control offers a second action next to it: **stop and discard the queue**. This
is a second stop in one action. It is the emergency brake the context asks for, and it works without timing. It is
shown only when there is something to discard, so the composer does not carry a permanent second button.

### Decision 4: what is discarded comes back, from the driver

The driver keeps the entries a second stop (or decision 3) discarded, per session, until the next discard replaces
them. It exposes them next to `queuedPrompts` in the session's messages payload, under a discard id. The
`/turn interrupt` response carries the same list, but the Console does not depend on it: a response lost to a closed
tab or a dropped connection is recovered by the next poll.

Each entry carries its text, its attachments and its source (decision 1). Only unsent input is here.

The Console shows a notice, for example "Stopped. 2 queued messages were discarded", with an action that puts the
member's own discarded text back into the input box, one message after another. It never sends. Discarded peer
messages are listed by sender in the same notice. They are not put back into the input box, because the member did
not write them. The action is offered once per discard id per tab. Two tabs can each put the text into their own
draft, but nothing is sent twice, because sending is the member's own act, and it gets a new `ClientMessageID`.

Limits: the kept entries live in the Agent's memory, so they are lost with an Agent restart, like the queue itself
(#1255). This decision gives no protection beyond that.

### Decision 5: the queue can be edited without stopping

The **Queued** bubbles in the chat get two actions: take the input back into the input box, and remove it. This is
the counterpart of claude's "Press up to edit queued messages", and the precise way to cancel one follow-up.

- Every queued entry has an id, and the id travels to the bubble; today `queuedPrompts` carries text only. The id is
  the entry's `ClientMessageID` where there is one, and a driver-minted id otherwise.
- A new `/turn` op removes an entry by id **only if it has not been sent**, atomically against the pump. It returns
  the entry, or `already_started` when the pump got there first. A stale bubble in another tab gets the same answer.
- The Console puts the text back into the input box only when the removal succeeded. Sending it again is a new send
  with a new `ClientMessageID`. The removed entry's own id is never reused, because drivers that record the ledger at
  accept time would drop the resend as a duplicate.
- Entries already sent to the runtime (decision 1) are shown but have no actions.

### Decision 6: Terminal (CLI) sessions keep the CLI's own behaviour

On the Terminal route, Stop stays an Esc to the CLI, and a second press is a second Esc. In the measured runs, claude
and codex continued into the queue on the first Esc and stopped that turn on the second. claude's rewind menu did
not open during a running turn. Nothing was left queued after the first Esc in those runs, so whether a second Esc
also discards further queued input was not observed. Agent Fleet does not add a queue or an episode of its own on
this route. opencode's Terminal behaviour differs (its queued input stays in the history) and is left as the CLI's.

### Decision 7: which other actions follow the rule

- **Rejecting a plan** and **launching a plan review elsewhere** send the Console's stop, so they follow decisions 1
  and 2 on every execution method.
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
  and codex's in-flight start. Decision 1 stops such input; it does not return it.
- Whether a stop aborts a turn this driver did not start (opencode: an attached TUI's turn). docs/log/126 §4 records
  the current inconsistency.

## Rejected

- **Keep the current rule** (discard own input, continue peer). It discards exactly the correction the member
  queued, and it disagrees with the Terminal route and with both major CLIs.
- **Put own input back into the input box on the first stop** (the recommendation before the measurements). It keeps
  the first stop a full brake, but it costs an extra action in the common "apply my correction now" case. It also
  makes Managed disagree with the Terminal route in the other direction. It survives as the recovery path in
  decision 4.
- **Two permanent controls** ("stop this turn" and "stop everything") shown all the time. They need space in a
  composer that already carries many controls, and they force a choice even when nothing is queued. Decision 3 shows
  the second one only when it would do something.
- **Press twice within N seconds.** Rejected in decision 2: it is unreliable over polling and mobile latency, and it
  cannot be expressed on the Terminal route anyway.
- **Merge the queue into one request, as claude did in the measured run.** Rejected in decision 1 for provenance.
- **Decide the second stop from the displayed turn state.** Rejected in decision 2: `accept` overwrites it.

## Consequences

- The plain first stop is no longer a full brake when something is queued. The full brake is decision 3's action,
  and decision 2's second stop, which needs no timing. The stop button's hint should say that a second press ends
  what was continued. The wording is left to the implementation, taken from the Console's own strings.
- For claude and codex, the chat now behaves the same on Terminal and Managed, within what was measured.
- The `Interrupt` contract changes in all seven Managed drivers: `KeptOnInterrupt` goes, `TurnInput` gains its
  source, and each driver gains the stop episode, the kept discard list, queue entry ids and the removal op.
  `/input` passes `peer_from` through, and the messages payload gains the ids and the discard list. The Console
  gains the stop control's second action, the notice and the bubble actions, and stops disabling Stop while a stop
  is pending. The member guide's sessions chapter (en/ja) states the two stops.
- The tests added in #1244 and #1258 that assert "own input is discarded by a stop" are inverted, not deleted: they
  become "own input continues after a first stop, and is discarded (and kept for return) by a second".
