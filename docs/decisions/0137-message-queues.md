# 0137. Message queues: the memo queue grows into per-repository queues that start sessions under a concurrency limit

English | [日本語](0137-message-queues.ja.md)

- Status: **accepted** (2026-11-14; proposed 2026-11-14). Nothing is built. Every `file:line` was read on
  `7ab3e1141`. Limit values are provisional.
- Tracking: #4145.
- Follow-ups: #4148, #4149, #4150, #4151, #4152 (steps 1 to 5 of §11); #4153 (open question 3).
- Related: [0021](0021-scheduled-execution.md) (scheduled execution; this record amends its decision 10 and its
  2026-10-03 addendum) / [0061](0061-work-item-inbox.md) (work-item inbox; amends decision 8) /
  [0111](0111-member-collaboration.md) (member collaboration) / [0113](0113-member-origin-mark.md) (the member's
  Console session stays out of the workspace) / [0073](0073-session-spawned-sessions.md) (sessions started by
  sessions) / [0120](0120-workspace-memory-pressure.md) (memory pressure) / [docs/log/21](../log/21-memo-queue.md)
  (the memo queue) / [docs/log/86](../log/86-session-fleet-observe.md) (what a session may not do)

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
- **The member's Console session stays out of the workspace** (ADR 0113 decision 6): every relay to the Agent drops
  the login cookie. A Control Plane route that requires that session cannot be called from inside the workspace,
  unless the member put the cookie there. (The member-origin mark of the same record is for writes the Control Plane
  relays to the Agent. Queues live on the Control Plane, so nothing here needs it.)
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
   can read that token (docs/build/07-security §7.2, "The limit"). The same token is accepted by every memo and
   category route on the token face, `PATCH`, `flush` and `DELETE` included (`control-plane/routes.go:610-618`):
   "a session cannot send" is held by the MCP layer alone, and anything in the workspace can rewrite a memo.
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
6. **A turn end is not a task end, and a stopped session is not a finished one.** A session that waits for CI or a
   review ends its turn, may be stopped while idle, and is started again by a notice (ADR 0133) or at a usage
   limit's reset (`control-plane/session_activity.go:149-152`).
7. **A session that failed at the API looks like one that finished**: idle, with a last turn end (measured,
   docs/log/87 §87.19.2).
8. **Sessions are heavy.** A parent and six children measured 1,761 to 1,904 MiB PSS, and no kind is lighter than
   claude (docs/log/87 §87.19.3).
9. **A scheduler tick waits for every fire it started** (`control-plane/scheduler.go:203-231`), and a cold wake
   takes minutes.

## Decision

Grow the memo queue in place. A memo is a message. A memo category becomes a **queue** when a person gives it a
standing instruction and a consumer. Fires ride the ADR 0021 scheduler and start a new session per fire, at most
`max_active` at once, counted from the state of the deliveries and their sessions and not from completion events.
What a session enqueues waits for one press by the person before it can be fired.

### 1. Queue, message, delivery

- **Queue.** A row of `memo_category` (already first-class, with a stable id) plus settings that only a person
  writes: the standing instruction (§5), the consumer (agent kind, model, effort, route, spend budget, worktree
  yes/no), the flow settings (§6) and the switches of §3 and §8. A category without settings, and the unfiled
  bucket, behave exactly as today: notes for the person, sent by the person. `add_memo`'s "a message to the HUMAN"
  stays true for them.
- **Message.** A `memo` row plus a state, a provenance (§3), an optional typed reference (§4) and a de-duplication
  key. States: `held` (waiting for the person's release), `pending` (may be fired), `claimed` (a fire took it),
  `delivered`, `unconfirmed` (the create's outcome is unknown and no session can be found), `superseded` (replaced by
  a tidy run, §8). From `claimed` on a message cannot be changed. A manual send keeps `sent_at` and is a delivery by a person.
- **Delivery.** One fire: its messages, the session it started, and a state: `claimed` (the messages are taken and
  nothing was sent), `sending` (the create request is leaving or has left), `open` (the session is known), `closed`
  with an outcome (clean, failed, aborted, unknown, or not sent). It is the ledger the Console shows and the thing
  the concurrency limit is counted from: every delivery that is not closed occupies one of the queue's `max_active`
  places (a slot, §6). Kept like the schedule run history.

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

- **Release is one press, bound to what was shown.** When a session enqueues into a queue, the Console shows one
  card per session and queue: which session, which queue, the items. Pressing it moves them to `pending`. The press
  is a Control Plane request under `/api`: it needs the member's Console session and passes the Console's write
  checks (`control-plane/internal/writegate`). The workspace does not hold that session (ADR 0113 decision 6), and
  the token face has no release route. So it is a wall, not a convention, as strong as the Control Plane's
  sign-in: a deployment that runs without sign-in has none (ADR 0113, Threat model). The release records a hash of
  what the card showed: the body, the reference, the attachments and the queue. A person who told a session "put
  these in the queue" is at the Console and presses once; what a session enqueues with nobody there waits.
- **A released message cannot be changed from inside the workspace.** A change to the body, the reference or the
  attachments, or a move to another category, that arrives by the token face or `/mcp` puts the message back to
  `held` and raises a new card (fact 1). The same change through the Console keeps the state: it is the member's
  own. On the token face a message in a queue cannot be flushed or deleted, and a category that is a queue cannot be
  renamed, merged or deleted; those stay with the Console. A rename that merges two categories keeps the surviving
  category's settings.
- **A per-queue switch removes the press for sessions the person started.** With "accept from sessions I started"
  on, a `workspace` message whose session has origin `user` enters as `pending`. The Agent supplies that
  attribution, so this switch is a guard rail, not a wall: with it on, any code in the workspace can enqueue without
  a press by claiming to be such a session (fact 1). Turning it on is the member accepting that for this queue. It
  is off by default.
- **Through the tools, never without a press**: a session that a queue, a schedule, another session, the operator or
  `/mcp` started; an `/mcp` client. So a queue does not feed itself by the tool path. With the switch off that holds
  against any code in the workspace; with it on it is a guard rail, for the reason above.
- **Nothing in the workspace can** create a queue, change its settings, release, or fire. Settings are written
  through the Console, with the member's session. The operator assistant is a `workspace` producer like any session.
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

- The **standing instruction** is written by the person in the queue's settings. It says what to do with an item:
  "implement the issue and open a PR", "verify this on the sandbox following the procedure in …", "file these ideas
  as issues". A queue without one cannot fire.
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

### 6. Fires: a concurrency limit, counted from state

**Settings**, per queue: `max_active` (default 2, at most 6), `batch` (messages per session: 1 by default, or all
that are pending up to a cap of 20), and when the queue is open: always, in a time window, or once K messages are
pending (then it drains until empty). A threshold waits until nothing was enqueued for a minute, so that ten
messages added in one go start together.

**The dispatcher** runs in the scheduler's tick, as a second source of due work beside `ListDueSchedules`. For each
open queue with `pending` messages it fires while slots are free, within the member-wide limit and the caps of §7.
A finished task is replaced at the next tick that completes. That is a minute when the workspace is running and
nothing else in the tick is slow; a cold wake in the same tick delays it by minutes (fact 9).

**Occupied is a level, not an event.** Every delivery that is not closed occupies a slot, from the claim on. An
`open` delivery closes, and frees its slot, only when nothing is running for it and nothing will start it again by
itself:

| The delivery's session, and the sessions that session started | Slot |
|---|---|
| A machine is working (a turn, a compaction, a background job) | occupied |
| A person has to act (question, plan, permission, sign-in, limit menu), running or stopped with the interaction carried | occupied |
| Waiting for a usage limit to lift, running or stopped with the resume booked | occupied, and no new fire for that agent kind |
| Idle or stopped, but holding a watch, a timer, or a child that is not finished | occupied |
| Idle or stopped with none of the above, seen on two consecutive ticks | free; the delivery closes |
| Archived or deleted | free; the delivery closes |

The first two rows read the state the reaper already classifies. The other rows need the Agent to report, on the
session row, what would start the session again: its watches and timers, a booked resume, and its children that are
not finished. The Control Plane keeps that report with the row in the database, so the answer is the same while the
workspace is stopped. A stopped session is not a finished task (fact 6); "nothing will start it again" is.

A slot held by a session that waits for a person is deliberate. Two sessions that stop on a question at night stop
the queue; the member finds two questions in the morning, not ten. A person who later resumes the session of a
closed delivery is doing their own work, and it is not counted again.

**Overdue.** A delivery open longer than `max_task_hours` (default 12) is marked overdue and the member is notified.
Being overdue never frees a slot. When neither its session nor any session that session started has shown progress
for an hour (the reaper's bound, `busyProgressLapse`), the dispatcher asks the Agent to **retire** them: end their
watches and timers, cancel their booked resumes, and stop them. A stop alone would not do: a watch or a booked
resume starts a stopped session again. The slot is then freed by the ordinary rule, when the Agent's report says
that nothing will start them again; a retire that does not get that far leaves the slot occupied. A delivery whose
children still make progress is not retired.

**A fire reserves, claims, then sends.**

1. *Reserve and claim*, in one transaction: check that the deliveries that are not closed are fewer than
   `max_active` on the queue's row, and fewer than the member-wide limit on a row per member, with a compare-and-set
   on a generation counter of each row; insert the delivery as `claimed`; move the chosen messages from `pending` to
   `claimed`. Two ticks, or two Control Plane tasks, that read the same free slot cannot both commit, even when they
   pick different messages. (The gate's claim covers one schedule's ledger; this one has to cover the queue and the
   member.)
2. *Wake and wait*, the scheduler's path.
3. *Send*: reserve the durable idempotency key `q_<delivery>` on the Agent; move the delivery from `claimed` to
   `sending` with a conditional write on its state, and only a fire whose write succeeds may go on; then create the
   session under that key with `idempotency_durable` set, the delivery id stamped on it, the queue's consumer
   settings, the `queue` source, and a **new worktree off the queue's repository** unless the queue says otherwise
   (two sessions in one working copy corrupt each other). When the answer arrives the delivery becomes `open` with
   the session's name.

The durable key is what the Agent already has for the launch from a handoff offer
(`workspace/agent/internal/sessionx/session_idempotency.go:14-23`; `control-plane/session_handoff.go:811-813` and
`reserveHandoffCreate` beside it). Its record is on disk. A key is reserved before its create is sent, and abandon
settles a reserved key under the same lock a create begins under: either the create began first and is reported, or
it is refused afterwards. The launch from the member inbox (`control-plane/inbox_launch.go`) is not the model: it
uses a plain key and a lookup.

What happens when a step does not complete:

- **Provably not sent**: the delivery never left `claimed` (the wake failed, the Agent was not ready in the window),
  or the create was answered with a refusal (memory pressure, a quota). The delivery is closed as not sent with a
  conditional write on its state, and only when that write succeeds do the messages return to `pending`. The queue
  backs off. It is not a failure.
- **Not provable**: the delivery is `sending` and no answer was recorded (a timeout, a Control Plane restart). It
  keeps its slot until reconciliation resolves it.

**Reconciliation** runs in every tick, before anything is reserved, over deliveries that are `claimed` or `sending`
and older than a fire's own time budget, so that a restart leaves nothing behind:

- `claimed`: close it as not sent, with the same conditional write on its state. If that write wins, the messages
  return to `pending`. If a slow fire's write to `sending` won instead, nothing is returned. Elapsed time alone
  never proves that an old worker will not send; the conditional write does.
- `sending`: abandon the key on the Agent (`POST /sessions-idempotency/{key}/abandon`) and act on its answer. A
  lookup is not enough: it answers the same `not_found` for a key that is only reserved and for one it has no record
  of, and a create under a reserved key may still begin
  (`workspace/agent/internal/sessionx/session_handlers.go:879-889`).
  - 200, the create is done: the delivery becomes `open` with the session the answer names.
  - 202, a create is in flight: the delivery keeps its slot; look again at the next tick.
  - 404 `not_created`, the key is abandoned or refused: nothing was created and nothing will be. The delivery closes
    as not sent and the messages return to `pending`.
  - 503 `create_outcome_unknown` (no record, an unreadable one, or a create cut off by an Agent restart): look for a
    session stamped with the delivery id. Found: the delivery becomes `open`. Not found: the delivery closes as
    unknown and the messages become `unconfirmed`. They are shown to the person and never fired again by
    themselves; a duplicate implements an issue twice.
  - No answer: nothing is decided, and the delivery keeps its slot.
- While the workspace is stopped a `sending` delivery cannot be resolved, and it keeps its slot: its session may
  exist and hold a watch or a booked resume. When such a delivery is all that keeps an open queue with `pending`
  messages from firing, the dispatcher wakes the workspace to resolve it. That wake counts against the queue's
  daily cap.

A delivery that closes stops its session and the sessions that session started (resumable); by the rule above
nothing is left that would start them again. Nothing is archived or deleted.

### 7. Safety valves

- **Circuit breaker.** The Agent reports, on the session row, how the delivery's first turn ended (clean, failed or
  aborted, which the instruction ledger already records per run: ADR 0021 addendum 2026-10-03) and how long it ran.
  Two deliveries in a row whose first turn failed or was aborted, or ended within a minute without a clean end
  (fact 7), pause the queue and notify the member. A first turn with no recorded outcome (a hook-less kind) counts
  only by the one-minute rule. A person resumes the queue.
- **Usage limit.** While a session of the queue is limited, no session of that kind is started.
- **Member-wide limit** on occupied slots across all queues (default 4), the `max_sessions` quota applied to queue
  fires (fact 5), and the Agent's refusal under memory pressure (ADR 0120 stage 2) respected.
- **Caps**: fires per day per queue (default 20) and the spend budget per session from the queue's settings.
- **Notifications**, through the Control Plane's notification centre like a schedule's: paused, every slot waiting
  for a person, unconfirmed, overdue.
- **Deletion guard**: an enabled queue blocks deleting its repository, as an enabled schedule does.

### 8. Tidying before a fire

Optional per queue, set by the person. When a fire is due and the queue has `pending` messages that no tidy run has
seen, the run happens after the wake and before the claim.

- **The call** is `OneShotDenyAll` with its own usage feature: no tool, no MCP server, no file, no network, verified
  by a probe and by the run's own output, and skipped when that cannot be shown.
- **The model proposes, Agent Fleet applies.** Input: the queue's `pending` messages as data, each with an id.
  Output: JSON operations over those ids: merge, split, move, keep. Agent Fleet validates the whole reply (every id
  was in the input, counts and sizes bounded, every move allowed by the rule below) and drops a malformed one whole.
- **A move cannot choose where work runs.** A tidy run may move a message into a category that is not a queue (it
  becomes a note for the person), or into a queue the person listed as a destination in this queue's tidy settings.
  A move into any other queue puts the message to `held`, and the release card names the destination. Another queue
  has another instruction and another budget; which of them a message runs under is the person's choice.
- **Nothing is lost.** A merge or a split creates new messages with `derived_from`; the sources become `superseded`
  and stay until retention. One press undoes a run while its results are not claimed.
- **Provenance survives.** Messages of different provenance, or from different producers, are never merged. A
  derived message is `curator` plus the provenance of its sources.
- **No reach.** The run cannot change settings, the consumer, or when a queue fires. A failed or skipped run fires
  the messages as they are.
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
  the same reservation and claim).
- **Control Plane**: settings and release under `/api`, requiring the member's Console session. On the token face:
  enqueue, list and edit, where an edit of a released message returns it to `held`; no release, no settings, and for
  a queue no flush, no delete and no category change.
- **Agent**: the relay for `add_memo`, the `queue` turn source, the delivery id on the session, retire, the report
  on the session row (what would start the session again, how the delivery's first turn ended), the tidy call. The
  durable idempotency key is used as it is.
- **Guide**: a task page, the limits table, and a term for the glossary. "Queue" already names a session's waiting
  inputs (ADR 0105) in the Console, so the word shown to members is chosen there, not here.

### 11. Steps

1. **Queues without automatic fires.** Settings, message states, provenance, the Agent relay, the release card and
   its hash, the rules for the token face, the envelope and source, the delivery ledger with reservation and
   reconciliation over the durable idempotency key, worktree per fire, "fire now".
2. **Automatic fires.** The dispatcher, the report on the session row, retire, the open conditions, the quota, the
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
- **A turn end, or a stop, as the task end.** Too early for a session that waits for CI or a review, and a stopped
  session is started again by a notice or a booked resume (fact 6).
- **Freeing a slot because the time is up, or because a stop was seen.** The children of an overdue session keep
  running, and a watch or a booked resume starts a stopped session again, so the rule that protects the limit would
  break it.
- **Closing a delivery whose create got no answer**, after a timeout or because the workspace is stopped. The
  session may exist; the slot would be handed out twice.
- **Letting sessions enqueue straight into automatic fires.** That is docs/log/86 §86.5's backdoor with a tool
  advertised to every session.
- **A credential per session to authenticate the producer.** Same uid: any process can read it (fact 1).
- **Delivering into an existing session.** No idempotency per fire, a Terminal session's input queue is invisible
  to Agent Fleet, and a long-lived consumer's context grows without bound (ADR 0021 decision 3).
- **The watch engine as the host** (ADR 0133). It tells the newest state per type and drops what came between,
  carries no free text, and does not run while the workspace is stopped.
- **`/chat/ask` or a scheduled operator turn as the tidy step.** The first is a chat turn that limits tools by a
  deny-list and is not verified to have none; the second has every memo tool, `flush_memos` and `create_session`.
- **A tidy run that may move a message into any queue.** It hands the model the choice of instruction and budget;
  a reply that names real ids and a real category passes validation whatever made the model choose it.
- **Tracker labels as the queue.** A gated schedule over a saved query already starts a session when a labelled
  issue appears (ADR 0021 addendum 2026-11-10). It stays the right tool when the tracker is the source of truth; it
  has no consumed state, no concurrency limit and no place for a note that is not an issue.
- **Redelivery after a timeout.** A session that did not report is not a session that did nothing.
- **Queues shared between members now.** A fire of one member's text in another's session contradicts ADR 0111
  decision 1 unless the recipient armed it.

## Consequences

- An unattended surface that sessions can reach exists for the first time. It is bounded by the person's press or
  standing switch, the standing instruction, the concurrency and daily caps, the spend budget and the breaker.
- The scheduler gets a second source of due work and shares its four parallel fires with schedules. Replenishment
  is as fast as the tick, and no faster.
- The Agent has to report, per session, what would start it again and how its first queue turn ended, and the
  Control Plane has to keep that with the row while the workspace is stopped. The Agent also gets a way to retire
  a session.
- A delivery whose create got no answer holds its slot until the Agent can be asked, which may take a wake.
- Hook-less agent kinds report idle by polling, which can be late or early, and record no turn outcome; for them
  the one-minute rule and the overdue rule are the backstops, and a queue's consumer is better a kind with turn
  hooks.
- Finished sessions and their worktrees accumulate until the person deletes them.
- The token face keeps accepting `flush` and `DELETE` for memos that are not in a queue (fact 1). This record does
  not widen that and does not close it.
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
5. **Faster replenishment.** Whether to take the re-count of slots out of the tick that waits for long fires
   (fact 9), so that a finished task is replaced within a minute whatever else is waking.

*Note, 2026-11-14 (acceptance).* Accepted by the owner as written. The open questions go with the work they belong
to: 1, 4 and 5 are carried in #4149, 2 in #4150, and 3 is #4153. Three sentences summarise §3 and are to be read
with it: the opening of the Decision ("What a session enqueues waits for one press by the person"), the end of §5
("came from the member or was released by the member") and the end of §8 ("derived only from messages the person
released"). §3 is the full rule. The messages that may be fired are the `pending` ones: those the member wrote, those
the member released, and those from a session the member started that were accepted without a press because the
member turned on that queue's standing switch, which is a guard rail and not a wall.
