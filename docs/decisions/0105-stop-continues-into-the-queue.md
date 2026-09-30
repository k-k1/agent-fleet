# 0105. A stop ends the running turn and moves on to the queue; a second stop ends the rest

English | [日本語](0105-stop-continues-into-the-queue.ja.md)

- Status: **proposed** (2026-09-30). The measurements are in [docs/log/127](../log/127-cli-stop-with-queued-input.md).
  The two-stage shape was the user's proposal in the #1258 discussion. The measurements then showed that claude
  and codex already behave this way.
- Follow-ups: #1282 (this decision), #1256 (the CLIs not yet measured)
- Supersedes: docs/log/27 §12.2-4 ("a stop reaches the queue"). Amends [0041](0041-cross-session-messaging.md)
  addendum 2026-09-30, decision 1 (only peer messages survive a stop).
- Related: docs/log/125 §4, docs/log/126 §4

## Context

The chat's **Stop the run (Esc)** means two different things today, depending on the execution method:

- **Terminal (CLI)**: it sends Esc to the CLI, and the CLI decides. Measured (docs/log/127): claude 2.1.285 and codex
  0.159.2 stop the running turn and **send the queued input at once as the next turn**. claude merges several
  queued inputs into one turn, and codex says so on screen: "press esc to interrupt and send immediately". A second
  Esc stops that turn. opencode 1.18.33 needs two Escapes, does not run the queued input, and keeps it in the history,
  where it rides along with the next prompt.
- **Managed**: every driver's `Interrupt` stops the turn and **discards the member's own queued input**. Only
  another session's message (`KeepOnInterrupt`, ADR 0041) survives and starts as the next turn.

The Managed rule was written before peer messages existed, and it gets the most common case wrong. People queue a
follow-up mostly to correct the running turn ("no, do X instead"). Stopping the turn so that the correction takes
effect now throws the correction away. The rule is also inconsistent: a peer message continues and the member's own
input does not. A member who switches the same session between Terminal and Managed gets opposite results from the
same button.

Pulling in the other direction, a single press must remain an emergency brake. A follow-up that assumes the stopped
turn succeeded ("then deploy it") must not quietly go on without a way to stop it.

The same `Interrupt` also backs things that are not labelled Stop: rejecting a plan, cancelling or denying a
question (codex), and launching a plan review elsewhere.

## Decision

### Decision 1: a first stop ends the running turn, and the queue continues

`Interrupt` stops the running turn and nothing else. What is queued, the member's own input and peer messages alike,
starts as the next turn once the stopped turn has settled. That is what the Managed drivers already do for peer
messages, and what claude and codex do for everything.

- Queued inputs keep running **one per turn**, in order. claude merges them into one turn. Merging is not adopted:
  a peer message's envelope and its `source: peer` in the transcript would blur into the member's text.
- Input that has left the queue but is not yet a turn (docs/log/126: muse's in-flight `turn/start`, opencode's
  input held behind another client's turn, codex before the `turn/start` answer) is part of the queue for this rule.
  So a first stop no longer cancels it; it continues.
- `KeepOnInterrupt` stops deciding anything and is retired. Whether an input came from a peer is still known from
  `peer_from`, and decision 3 uses it for display only.

### Decision 2: a second stop ends what the first stop continued, and discards the rest

A stop is a **second stop** when it arrives while either of these holds:

- the first stop is still settling (the turn state is `interrupting`), or
- the running turn is one that the queue started **as a direct result of a stop**, with no new input from the
  member in between.

A second stop stops that turn and **discards everything still queued**, peer messages included. It is decided by
state in the driver, **not by time**: the Console's poll, a phone's latency and a slow model all make "twice within
N seconds" unreliable, and a member who notices only after the continued turn has run for a minute must still be able
to stop it.

A second stop that finds no queue is simply a first stop. Stopping any other turn is a first stop again.

### Decision 3: what is discarded comes back

`POST /sessions/{name}/turn {"op":"interrupt"}` answers with what it discarded. On a first stop the list is empty.
Each entry carries the text, the attachments and, for a peer message, the sending session.

The Console shows a notice after a second stop, for example "Stopped. 2 queued messages were discarded", with an
action that puts the member's own discarded input back into the input box. The action puts back all of the
discarded text, one message after another. It does not send anything. Discarded peer messages are listed by sender
in the same notice. They are not put back into the input box, because the member did not write them.

This is what makes a second stop safe to use as "stop everything": nothing the member wrote is lost for good, and
nothing a peer sent vanishes without a trace on the receiving side.

### Decision 4: the queue can be edited without stopping

The **Queued** bubbles in the chat get two actions: take the input back into the input box, and remove it. This is
the counterpart of claude's "Press up to edit queued messages". It is the direct way to cancel a follow-up before
pressing Stop, so that a member does not have to reason about decisions 1 and 2 to get rid of one queued line.

Every driver's queue entries get an id for this. The `ClientMessageID` is used where there is one, and the driver
mints an id where there is not. A new `/turn` op removes an entry by id. Taking an entry back is a removal followed by
putting its text into the input box in the Console.

### Decision 5: Terminal (CLI) sessions keep the CLI's own behaviour

On the Terminal route, Stop stays an Esc to the CLI, and a second press is a second Esc. claude and codex then do what
decisions 1 and 2 describe (measured; a second Esc during a running turn does not open claude's rewind menu).
Agent Fleet does not add a queue of its own there. opencode's Terminal behaviour differs (its queued input stays in
the history) and is left as the CLI's.

### Decision 6: every interrupt follows the same rule

Rejecting a plan, cancelling or denying a question, and launching a plan review elsewhere go through `Interrupt`,
so they follow decisions 1 and 2 as well. That matches the Terminal route, where each of them is an Esc.

### Decision 7: teardown is unchanged

Halt, archive, recreate, switching the execution method (`DropHandle`), Agent shutdown (`AbortManaged`) and a daemon
drain still discard the whole queue. There is no runtime left to run it on.

### Out of scope

- A durable queue that survives an Agent restart (#1255).
- Notifying the sending session that its peer message was discarded. Decision 3 makes the discard visible on the
  receiving side only. docs/log/125 §4 rejected a sender notification for its protocol cost, and that still stands.
- Whether a stop aborts a turn this driver did not start (opencode: an attached TUI's turn). docs/log/126 §4 records
  the current inconsistency.

## Rejected

- **Keep the current rule** (discard own input, continue peer). It discards exactly the correction the member
  queued, and it disagrees with the Terminal route and with both major CLIs.
- **Put own input back into the input box on the first stop** (the recommendation before the measurements). It keeps
  the first stop a clean brake, but it costs an extra action in the common "apply my correction now" case. It also
  makes Managed disagree with the Terminal route in the other direction. It survives as the recovery path in
  decision 3.
- **Two separate controls** ("stop this turn" and "stop everything"). This is explicit, but it needs space in a
  composer that already carries many controls, and on a phone it forces a choice under pressure. Decision 4 covers
  the precise case (remove one queued line) better.
- **Press twice within N seconds.** Rejected in decision 2: it is unreliable over polling and mobile latency, and it
  cannot be expressed on the Terminal route anyway.
- **Merge the queue into one turn, as claude does.** Rejected in decision 1 for provenance.

## Consequences

- The first stop is no longer a full brake when something is queued. The mitigations are decision 2 (the second stop
  needs no timing) and decision 4 (queued lines can be removed before stopping). The stop button's hint should say
  that a second press ends what was continued. The wording is left to the implementation, taken from the Console's
  own strings.
- Behaviour of the chat now matches between Terminal and Managed for claude and codex.
- The `Interrupt` contract changes in all seven Managed drivers (`KeptOnInterrupt` goes). The `/turn interrupt`
  response gains a body, and the drivers' queues gain entry ids and a removal op. The Console gains the notice and the
  bubble actions. The member guide's sessions chapter (en/ja) states the two stops.
- The tests added in #1244 and #1258 that assert "own input is discarded by a stop" are inverted, not deleted: they
  become "own input continues after a first stop, and is discarded (and returned) by a second".
