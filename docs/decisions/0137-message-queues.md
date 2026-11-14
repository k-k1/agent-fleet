# 0137. Message queues: the memo queue grows into per-repository queues that start sessions under a concurrency limit

English | [日本語](0137-message-queues.ja.md)

- Status: **proposed** (2026-11-14). Nothing is built. Every `file:line` was read on `7ab3e1141`. Limit values are
  provisional.
- Tracking: #4145.
- Related: [0021](0021-scheduled-execution.md) (scheduled execution; this record amends its decision 10 and its
  2026-10-03 addendum) / [0061](0061-work-item-inbox.md) (work-item inbox; amends decision 8) /
  [0111](0111-member-collaboration.md) (member collaboration) / [0113](0113-member-origin-mark.md) (member-origin
  mark) / [0073](0073-session-spawned-sessions.md) (sessions started by sessions) /
  [0120](0120-workspace-memory-pressure.md) (memory pressure) / [docs/log/21](../log/21-memo-queue.md) (the memo
  queue) / [docs/log/86](../log/86-session-fleet-observe.md) (what a session may not do)

## Context

The memo queue (docs/log/21) holds notes per member, grouped by repository folder and a free-form category. A
person collects them, from any device and while the workspace is stopped, and later sends a selection into a session
as one message. Sessions may add and correct notes; deleting and sending stay with the person
(`workspace/agent/internal/mcpx/mcp_fleet_observe.go:41-53`). The tool text says so: `add_memo` is "a message to the
HUMAN".

What is wanted is a cycle that runs without the person pressing Send each time:

- A person puts work into a queue, or tells a session to put it there.
- The queue starts sessions for that work by itself.
- At most N such sessions run at once; when one finishes its task, the next message is started in its place.
- Before a fire, an agent may tidy what is waiting: classify, merge duplicates, split a note that holds three things.

The examples that drove this: GitHub issues that are ready to implement go into a queue as they are found; issues
that need a check on the sandbox collect during the day and run at night; the day's ideas are sorted and filed as
issues in the evening.

### What already exists

- **Scheduled execution** (ADR 0021, `control-plane/scheduler*.go`). A tick every minute lists what is due and fires
  up to four at once (`scheduler.go:196`). A fire resolves the member, holds the reaper off, wakes a stopped
  workspace, waits for the Agent, and creates a session. The 2026-11-10 addendum added a gate: a cheap check before
  the fire, a claim written before the inject so that two ticks cannot both fire, and caps per day.
- **A classification of every session row** for the reaper (`control-plane/session_activity.go:118`): a machine is
  working, a person has to act, the turn is over, or unknown. The same rows are kept in the database while the
  workspace is stopped, with the interaction that was pending (ADR 0055).
- **The member-origin mark** (ADR 0113): the Control Plane can tell that a write came from the member through the
  Console, which nothing inside the workspace can forge.
- **A one-shot model call verified to have no tools** (`workspace/agent/internal/chatx/chat_denyall.go:176`,
  ADR 0117 decision 3). It has no production caller yet.
- **A spend budget per session** and **a work item per session** on `create_session`.

### Earlier decisions this touches

| Record | What it says | Why it matters here |
|---|---|---|
| docs/log/86 §86.5 | Sessions get neither `flush_memos` nor the schedule tools: schedules outlive the session and wake a stopped workspace, "a persistent backdoor a poisoned repository can plant" | A queue a session can feed and that fires by itself is those two things added together |
| ADR 0021 decision 10 | The prompt of an unattended run carries no data, only fixed metadata (and, since the gate, validated item keys) | A queue carries text someone wrote into an unattended run |
| ADR 0021 addendum 2026-10-03 | Chaining one run's output into the next is out of scope | A tidy step's output becomes part of the next prompt |
| ADR 0061 decision 8 | Work items are not merged into the memo queue: "memos are editable and items are read-only; mixing them muddies both" | Two of the three examples queue issues |
| ADR 0111 decisions 1, 3, 11 | Nothing a member sends makes another member's agent act; a taken-in memo is sent on its own; agents draft, people send | Bounds who may feed a queue |

### Facts that shape the design

1. **The Control Plane knows which member enqueued, not which session.** `AF_MEMO_TOKEN` carries the membership only
   (`control-plane/memo_bridge.go:29-62`), and the MCP process posts the model's arguments to the Control Plane
   directly (`workspace/agent/internal/mcpx/mcp_client.go:30`). Any process in the workspace runs as the same uid and
   can read that token (docs/build/07-security §7.2, "The limit"). The same token is accepted by
   `POST /internal/memos/flush` and `DELETE /internal/memos/{id}` (`control-plane/routes.go:612-614`): "a session
   cannot send" is held by the MCP layer alone.
2. **A flush reads as the member typing.** `sendMemoPrompt` posts `{op:start, prompt}` with no source
   (`control-plane/memo.go:658`), so the turn has no envelope and no origin. That is right for a person's press and
   wrong for anything automatic.
3. **Only the Control Plane is alive while a workspace is stopped** (ADR 0021 Context). Enqueueing already works
   then; deciding to fire has to live there too.
4. **There is no project entity.** `memo.repo` is a trimmed string (`control-plane/memo.go:165`), in practice a
   folder name under `~/repos`, so a note added from a worktree can file under `repo@slug`, a bucket of its own.
5. **A scheduled new session gets the directory verbatim and no worktree** (`control-plane/scheduler_wake.go:410`),
   and the `max_sessions` quota is checked only on the Console's create and fork
   (`control-plane/workspace_handlers.go:1201,1236`).
6. **A turn end is not a task end.** A session that waits for CI or a review ends its turn and is started again by a
   notice (ADR 0133).
7. **A session that failed at the API looks like one that finished**: idle, with a last turn end (measured,
   docs/log/87 §87.19.2).
8. **Sessions are heavy.** A parent and six children measured 1.76 to 1.90 GiB PSS, and no kind is lighter than
   claude (docs/log/87 §87.19.3).

## Decision

Grow the memo queue in place. A memo is a message. A memo category becomes a **queue** when a person gives it a
standing instruction and a consumer. Fires ride the ADR 0021 scheduler and start a new session per fire, at most
`max_active` at once, counted from the state of the sessions and not from completion events. What a session
enqueues waits for one press by the person before it can be fired.

### 1. Queue, message, delivery

- **Queue.** A row of `memo_category` (already first-class, with a stable id) plus settings that only a person
  writes: the standing instruction (§5), the consumer (agent kind, model, effort, route, spend budget, worktree
  yes/no), the flow settings (§6) and the switches of §3 and §8. A category without settings, and the unfiled
  bucket, behave exactly as today: notes for the person, sent by the person. `add_memo`'s "a message to the HUMAN"
  stays true for them.
- **Message.** A `memo` row plus a state, a provenance (§3), an optional typed reference (§4) and a de-duplication
  key. States: `held` (waiting for the person's release), `pending` (may be fired), `claimed` (a fire took it),
  `delivered`, `unconfirmed` (the fire could not tell whether it arrived), `superseded` (replaced by a tidy run,
  §8). A manual send keeps `sent_at` and is a delivery by a person.
- **Delivery.** One fire: its messages, the session it started, and a state (`claimed`, `open`, `closed` with an
  outcome of clean, failed, aborted or unknown). It is the ledger the Console shows and the thing the concurrency
  limit is counted from. Kept like the schedule run history.

A rename that merges two categories keeps the surviving category's settings.

### 2. The key is the base repository, per member

A queue is (membership, repository, category). The repository is the base repository's folder name: an enqueue that
names a worktree folder is filed under its base (`gitx.WorktreeParent` on the Agent, `folderBase` in the Console).
When the remote can be normalised, `host/owner/repo` is stored beside it (the form `canonicalRepoIdentity` produces,
`control-plane/inbox_recipes.go:49`); it is not the key, and exists so that a later record can share a queue between
members without a migration of meaning. Queues are a member's own (§9).

### 3. Who may enqueue, and what may be fired without a press

The Control Plane sets a message's provenance from the route it arrived by, never from its arguments:

| Provenance | Route | Enters as |
|---|---|---|
| `member` | Console, with the member's session | `pending` |
| `workspace` | The queue token, relayed by the Agent with the session's name and origin | `held` |
| `mcp` | `/mcp`, with the connection id | `held` |
| `inbox` | Taken in from the member inbox (origin columns, ADR 0111) | never fireable; sent by the person, alone |
| `curator` | Derived by a tidy run (§8) | the state of its sources |

- **Release is one press.** When a session enqueues into a queue, the Console shows one card per session and queue:
  which session, which queue, the items. Pressing it moves them to `pending`. The press carries the member-origin
  mark (ADR 0113), so it is a wall, not a convention. A person who told a session "put these in the queue" is at
  the Console and presses once; what a session enqueues with nobody there waits.
- **A per-queue switch removes the press for sessions the person started.** With "accept from sessions I started"
  on (set with the mark), a `workspace` message whose session has origin `user` enters as `pending`. The Agent
  supplies that attribution, so this switch is a guard rail, not a wall (fact 1). It is off by default.
- **Never without a press**: a session that a queue, a schedule, another session, the operator or `/mcp` started;
  an `/mcp` client. A queue therefore cannot feed itself.
- **A session cannot** create a queue, change its settings, release, delete, or fire. Settings are written through
  the Console with the mark. The operator assistant is a `workspace` producer like any session.
- **No new always-on tool.** A session enqueues with `add_memo` into a category that is a queue; `list_memos` marks
  which categories are queues. `add_memo` goes through the Agent instead of straight to the Control Plane, so the
  Agent stamps the session. The description changes; the tool count does not.
- **Bounds** of the kind peer messages have (`workspace/agent/internal/sessionx/session_peer.go:37-48`): a rate
  limit per producer, a size cap, an identical message dropped, and a body that starts with `[agent-fleet:` refused.

This amends docs/log/86 §86.5 narrowly: a session may now cause an unattended run, but only through a queue a person
configured, and only with a person's press or that person's standing switch.

### 4. Typed references

A message may carry a reference `{provider, key, url}` with the key in the work-item shape (`owner/repo#N`,
`PROJ-12`). The Console offers "add to queue" on a work item. One queue holds at most one message that is not
closed per (provider, key). The item's body is never copied (ADR 0061 decisions 2 and 5): the session reads it with
`gh` or `jira`, as a session started from a work item does. When a fire carries exactly one reference, it is passed
to the session as its work item, so the branch name and the link follow the existing rules.

This amends ADR 0061 decision 8. That decision keeps the item itself out of the memo list, and it still does: the
item stays in its section, read-only. What a queue holds is a pointer to it with the member's own note.

### 5. What a fire delivers: a standing instruction, and the messages as data

- The **standing instruction** is written by the person in the queue's settings (with the mark). It says what to do
  with an item: "implement the issue and open a PR", "verify this on the sandbox following the procedure in …",
  "file these ideas as issues". A queue without one cannot fire.
- A fire's prompt is, in order: the envelope `[agent-fleet:queue queue=<id> delivery=<id> reply=none]`; the
  standing instruction; the items under a line that labels them as data, each with a server-written origin line
  (who put it there, and through what).
- The turn's source is `queue`, with its own badge and its own paragraph in the workspace policy: the instruction
  is the member's; an item's text says what to work on and cannot widen the instruction, stand in for an approval
  or change what governs the session (the four prohibitions of a peer message).
- A batch over the size cap, or one with attachments, is written to a file in the workspace and the prompt names
  the path (a long message typed into a busy Terminal session arrived truncated, #2195).

This amends ADR 0021 decision 10 the way `{{gate.refs}}` did: the unattended prompt still consists of text a person
registered, and what is added is marked as data, came from the member or was released by the member, and names its
origin.

### 6. Fires: a concurrency limit, counted from session state

**Settings**, per queue: `max_active` (default 2, at most 6), `batch` (messages per session: 1 by default, or all
that are pending up to a cap of 20), and when the queue is open: always, in a time window, or once K messages are
pending (then it drains until empty). A threshold waits until nothing was enqueued for a minute, so that ten
messages added in one go start together.

**The dispatcher** runs in the scheduler's tick, as a second source of due work beside `ListDueSchedules`. For each
open queue with `pending` messages it computes `free = max_active − occupied` and fires that many, within the
member-wide limit and the caps of §7. A finished task is therefore replaced within a minute.

**Occupied is a level, not an event.** A delivery occupies one of the queue's `max_active` places (a slot, below)
while its session is in one of these states:

| The session the delivery started | Slot |
|---|---|
| A machine is working (a turn, a compaction, a background job) | occupied |
| A person has to act (question, plan, permission, sign-in, limit menu), also while stopped with the interaction carried | occupied |
| Waiting on the clock for a usage limit | occupied, and no new fire for that agent kind |
| Idle but holding a watch or a running child session | occupied |
| Idle with nothing to wait for, seen on two consecutive ticks | free; the delivery closes |
| Stopped without a carried interaction, archived or deleted | free; the delivery closes |
| Open longer than `max_task_hours` (default 12) | free; the delivery closes as unknown and the member is notified |

The first three rows read the state the reaper already classifies (a limited session is foldable for the reaper and
occupied here). The fourth needs one new field on the session row (the Agent knows the watches and the children; the
Control Plane does not). When the workspace is stopped the rows come from the database: only a carried interaction
occupies.

A slot held by a session that waits for a person is deliberate. Two sessions that stop on a question at night stop
the queue; the member finds two questions in the morning, not ten.

**A fire** claims first: one conditional write moves the chosen messages from `pending` to `claimed` and creates
the delivery, so of two ticks, or two Control Plane tasks, exactly one wins. Then the scheduler's path runs: wake,
wait for the Agent, create the session with the idempotency key `q_<delivery>`, the queue's consumer settings, the
`queue` source, and a **new worktree off the queue's repository** unless the queue says otherwise (two sessions in
one working copy corrupt each other).

- **Provably not delivered** (the wake failed, the Agent was not ready in the window, the Agent refused because of
  memory pressure or a quota): the messages return to `pending` and the queue backs off. It is not a failure.
- **Not provable** (the create was sent and no answer came): the messages become `unconfirmed`. They are shown to
  the person and never fired again by themselves. A duplicate implements an issue twice.
- A delivery that closes stops its session (resumable). Nothing is archived or deleted.

### 7. Safety valves

- **Circuit breaker.** Two deliveries in a row that close as failed or aborted, or that close within a minute of
  starting without a clean turn (fact 7), pause the queue and notify the member. A person resumes it.
- **Usage limit.** While a session of the queue is limited, no session of that kind is started.
- **Member-wide limit** on occupied slots across all queues (default 4), the `max_sessions` quota applied to queue
  fires (fact 5), and the Agent's refusal under memory pressure (ADR 0120 stage 2) respected.
- **Caps**: fires per day per queue (default 20) and the spend budget per session from the queue's settings.
- **Notifications**, through the Control Plane's notification centre like a schedule's: paused, every slot waiting
  for a person, unconfirmed, expired.
- **Deletion guard**: an enabled queue blocks deleting its repository, as an enabled schedule does.

### 8. Tidying before a fire

Optional per queue, set by the person. When a fire is due and the queue has `pending` messages that no tidy run has
seen, the run happens after the wake and before the claim.

- **The call** is `OneShotDenyAll` with its own usage feature: no tool, no MCP server, no file, no network, verified
  by a probe and by the run's own output, and skipped when that cannot be shown.
- **The model proposes, Agent Fleet applies.** Input: the queue's `pending` messages as data, each with an id.
  Output: JSON operations over those ids: merge, split, move to another category of the same repository, keep.
  Agent Fleet validates the whole reply (every id was in the input, counts and sizes bounded, a move names an
  existing category) and drops a malformed one whole.
- **Nothing is lost.** A merge or a split creates new messages with `derived_from`; the sources become `superseded`
  and stay until retention. One press undoes a run while its results are not claimed.
- **Provenance survives.** Messages of different provenance, or from different producers, are never merged. A
  derived message is `curator` plus the provenance of its sources.
- **No reach.** The run cannot change settings, the consumer, or which queue fires when. A failed or skipped run
  fires the messages as they are.
- **Bounds**: one run at a time per member, two minutes per call, a daily cap.

The Console's existing Tidy (preview, then approve) gains the same operations and stays the manual path.

This amends the 2026-10-03 addendum of ADR 0021: the text that reaches the next prompt is derived only from messages
the person released, by a call that has no tools, and validated; it is not the output of a session.

### 9. One member

Queues belong to one member. Another member's text reaches a queue only by take-in from the inbox, which stays
manual and alone (ADR 0111 decisions 1 and 3). A queue shared by the members of a project needs its own record.

### 10. Surfaces

- **Console**: the memo section marks queues and shows each message's state and origin; a settings dialog; the
  release card; the deliveries with links to their sessions; pause, resume and "fire now" (a person's press, through
  the same claim).
- **Control Plane**: settings and release under `/api`, requiring the mark. On the token face, enqueue and list only.
- **Agent**: the relay for `add_memo`, the `queue` turn source, the new field on the session row, the tidy call.
- **Guide**: a task page, the limits table, and a term for the glossary. "Queue" already names a session's waiting
  inputs (ADR 0105) in the Console, so the word shown to members is chosen there, not here.

### 11. Steps

1. **Queues without automatic fires.** Settings, message states, provenance, the Agent relay, the release card, the
   envelope and source, the delivery ledger, worktree per fire, "fire now".
2. **Automatic fires.** The dispatcher, `max_active`, the session-row field, the open conditions, the quota, the
   breaker, the caps and the notifications.
3. **Typed references** and "add to queue" on work items.
4. **"Accept from sessions I started".**
5. **Tidying before a fire.**

## Rejected

- **A new queue store beside the memo queue.** Two places to note things, two sections, two sync paths; the memo
  queue already has the grouping, the devices and the token face.
- **Firing on a count or a time alone**, one batch into one session. It cannot bound how many sessions run, and a
  backlog either waits for the next slot or lands in one session's context.
- **Releasing a slot on a completion event** (`af_report`, a turn-end callback). One lost event holds the slot for
  ever; the report ledger moved from an edge to a level for the same reason (ADR 0035).
- **A turn end as the task end.** Too early for a session that waits for CI or a review (fact 6).
- **Letting sessions enqueue straight into automatic fires.** That is docs/log/86 §86.5's backdoor with a tool
  advertised to every session.
- **A credential per session to authenticate the producer.** Same uid: any process can read it (fact 1).
- **Delivering into an existing session.** No idempotency per fire, a Terminal session's input queue is invisible
  to Agent Fleet, and a long-lived consumer's context grows without bound (ADR 0021 decision 3).
- **The watch engine as the host** (ADR 0133). It tells the newest state per type and drops what came between,
  carries no free text, and does not run while the workspace is stopped.
- **`/chat/ask` or a scheduled operator turn as the tidy step.** The first is a chat turn that limits tools by a
  deny-list and is not verified to have none; the second has every memo tool, `flush_memos` and `create_session`.
- **Tracker labels as the queue.** A gated schedule over a saved query already starts a session when a labelled
  issue appears (ADR 0021 addendum 2026-11-10). It stays the right tool when the tracker is the source of truth; it
  has no consumed state, no concurrency limit and no place for a note that is not an issue.
- **Redelivery after a timeout.** A session that did not report is not a session that did nothing.
- **Queues shared between members now.** A fire of one member's text in another's session contradicts ADR 0111
  decision 1 unless the recipient armed it.

## Consequences

- An unattended surface that sessions can reach exists for the first time. It is bounded by the person's press or
  standing switch, the standing instruction, the concurrency and daily caps, the spend budget and the breaker.
- The scheduler gets a second source of due work and shares its four parallel fires with schedules.
- Hook-less agent kinds report idle by polling, which can be late or early; for them `max_task_hours` is the
  backstop, and a queue's consumer is better a kind with turn hooks.
- Finished sessions and their worktrees accumulate until the person deletes them.
- The token face keeps accepting `flush` and `DELETE` for memos (fact 1). This record does not widen that and does
  not close it.
- On acceptance, a note goes into ADR 0021 and ADR 0061 pointing here.

## Open questions

1. **The defaults**: `max_active` 2 (at most 6), 4 member-wide, 20 fires a day, 12 hours. They are guesses from
   docs/log/87's memory figures and need a measurement of real queue runs.
2. **References without a press.** A message from a session that is only a reference carries no free text. Whether
   it may enter as `pending` without the press depends on whether the tracker's content is trusted, which differs
   per repository.
3. **A pull tool** for a session a person is driving ("take the next five from the queue"), and whether an
   existing session can ever be a consumer.
4. **A limit on finished work nobody looked at**: stop firing while more than M closed deliveries wait for the
   person.
