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

*Note, 2026-11-15 (step 1: #4148, built as #4157, #4177, #4173 and #4172).* Step 1 of §11 is merged. Nothing has run
on a deployment yet. Building it settled the following, which the text above does not say or says differently.

- **A tenant switch, off by default** (the owner's decision). `tenant.memo_queues` gates creating or changing a
  queue's settings; an administrator turns it on. The rules for the token face and `/mcp` are always on, whatever the
  switch says, and a later step makes the dispatcher skip a tenant whose switch is off. **Rollout rule: turn it on
  only after every Control Plane of the deployment runs a version that has the rules.** No queue can exist before
  that, so an older binary, which knows none of §3, never sees one. Turning it off later deletes nothing.
- **What "is a queue" means.** A category is a queue when it has a settings row, enabled or not. `enabled` gates
  automatic fires only; "fire now" needs an instruction and not `enabled`. A category of the common bucket can be a
  queue; it gets no directory and no worktree.
- **Entering a queue.** `pending` only for a message of provenance `member` that arrives by the Console; `held`
  otherwise. The notes already in a category when it becomes a queue enter by the same rule: an unsent note the
  member wrote in the Console becomes `pending`, and a row from before this step, whose provenance is unknown and is
  treated as `workspace`, becomes `held`. A note that was already sent by hand enters as `delivered`. A category that
  holds a note taken in from the inbox cannot become a queue.
- **The rate limit is per member, not per producer.** §3 bounds each producer. What was built counts every create
  into a queue from the token face and `/mcp` together, across all of the member's queues: 30 a minute
  (provisional). Moving or deleting a message does not give its create back, and a create answered with an existing
  identical message is not counted. One busy session can therefore use up the minute for the member's other sessions
  and connections.
- **A release is all or none.** One press releases every item it names or none: one stale item refuses the press
  and returns the stale ids. §3 reads as if each item stood alone.
- **A second guard on a released message.** A `pending` row keeps a copy of the released content under a database
  constraint, so that no write, by this binary or an older one, can leave a `pending` row whose content is not what
  was released. The fire checks the hash again at the claim.
- **A manual send reserves, sends, then settles.** The send marks its rows before it sends and settles only the rows
  that still carry its mark; an unknown outcome makes a queue message `unconfirmed`. While a row is marked, nothing
  may make it a queue message, take it out of a queue, or change it inside one. A fire claims only `pending` rows
  that were never sent and carry no such mark.
- **The reservation of §6 uses the member's row lock, not a generation counter.** Every memo write of a member
  already runs behind a lock on the membership row, and both limits (the queue's `max_active` and the member-wide 4)
  belong to one member, so they are counted and taken behind that lock. Two Control Plane processes racing over one
  database reserve exactly as many slots as there are, on SQLite and on Postgres. The quota on sessions is checked
  at the press and again, behind the same lock, when the delivery moves to `sending`.
- **Closing, for now.** Step 1 closes an `open` delivery when its session is archived or deleted (outcome aborted),
  stopped with nothing carried, or idle on two consecutive ticks (outcome clean). An Agent that does not answer, or
  a session that is starting or stopping, is no evidence of either. **Closing stops no session yet**: whether a stop
  is safe depends on the report of what would start the session again, which is step 2 (#4149), together with the
  rest of the table in §6, overdue and retire, and the wake made only to reconcile.
- **The Agent composes the prompt.** The Control Plane sends the delivery as data (instruction and items); the Agent
  writes the envelope, the instruction as it is, a line that labels data, and every line of every item quoted,
  under an origin line built from structured fields that says who added the item, not who released it. Over 8 KiB,
  or with an attachment, the items go to a file in the session's own work directory. A prompt that starts with the
  queue envelope is refused on every input path that takes text (create, input, turn, peer, `/mcp`, the chat bridge).
  Limits: 20 items, 16 KiB of instruction, 64 KiB per item. An explicit refusal by the Agent is settled at once by
  abandoning the key, as §6 says.
- **Attribution.** The Agent stamps the session's name and origin on a create; a value the model passes is ignored
  and an unknown caller is refused. The operator assistant is stamped as `operator`.
- **Words on screen** (working terms; §10 leaves the choice to the Console). Members do not see "queue" or "slot": a
  category has **auto-run** settings; a note waits for approval, waits to run, is starting, is sent, or needs a
  look; a run is starting, running, done, failed, stopped, needs a look, or was not sent. The release card is inline
  in the memo section, not a notification.

Left out of step 1 and tracked: filing a note added from a worktree under its base repository (§2, #4175); the guide
page, limits and glossary (#4188); moving the new error codes beside the others (#4187).

*Note, 2026-11-15 (step 2: #4149, built as #4200, #4216, #4233 and #4240).* Step 2 of §11 is merged. Step 1 was seen
on a deployment the same day (#4148); nothing of step 2 has run on one yet. The points below are where building it
made the text above concrete or departed from it. Where a point only confirms §6 or §7, it says so.

- **What the Agent reports.** Per session row: `wakers` (its watches and its timers, built-in subscriptions and a
  notice being delivered included), or `wakersUnknown` when the Agent cannot tell. §6 also names a booked resume and
  the unfinished children: those are not reported again; the Control Plane reads them from fields the row already
  had (`rateLimitResumeAt`, `origin` and `originSession`). The answer to the session list carries `queueReport: 1`,
  because a zero count is omitted and an older Agent sends nothing either. For a workspace without the mark the
  Control Plane keeps step 1's closing rule, stops no session, and starts no run by itself.
- **The first turn (§7).** The instruction ledger does not record it; the Agent keeps a record of its own, written
  once. It is confirmed only by the identity of the delivery's own input, and absent in every other case: another
  input started first, the Agent restarted before the turn was confirmed, the agent kind rewrites the identity
  (opencode). A missing first turn costs the breaker one data point; a wrong one would report a lost delivery as
  finished.
- **Retire (§6).** Only for a session that no session started, which a delivery's own session always is. One retire
  per root runs at a time, the recovery after an Agent restart included. The mark carries the retire's identity, and
  a person's start clears it in one write that also records which retire the session was released from. A retire
  never touches a session a person released from it, nor anything under such a session, and its answer says so
  (`released`, and `complete` only when every session was retired). While a session is retired and stopped the Agent
  starts it for nothing and refuses input to it (`session_retired`); a person starts it from the Console and it is an
  ordinary session again. Not covered: a person's start between the Control Plane's read and its first retire call
  (#4218).
- **Occupancy is kept with the delivery (§6).** The last report read is stored on the delivery: why it is held and
  in which session, and when progress was last seen. Reads are ordered by a number the database hands out, never by
  a clock. Keeping a read, counting it and closing on it are one transaction behind the member's row lock. A delivery
  closes on the second read in a row with nothing holding it. While the workspace is stopped only a delivery last
  seen with nothing holding it may close.
- **Closing, then stopping.** A clean close records that a retire is owed. A retire the Agent has not confirmed is
  repeated on later ticks, and a stopped workspace is not woken for the retire of a delivery that has closed. The
  repeats end with a result kept on the delivery: the Agent said `complete`; the Agent said the root was released (a
  person has the session); the Agent reported released children and confirmed that every other session is stopped
  with nothing left unended; the Agent refused the session as not a root, which is a fault and is logged; or the
  session is missing from a list read after the close. A 404 is not a confirmation.
- **Two versions side by side.** A database constraint refuses a clean close of a delivery whose last report holds
  it, so a Control Plane older than step 2 cannot free that slot by its own rule. The count of clear reads does not
  trust a count the older rule wrote. Rollout rule, with step 1's: turn the tenant switch on only when every Control
  Plane runs the version with these migrations and the Console that words the new notifications is deployed.
- **The dispatcher (§6).** It runs in the tick after the settle pass, with a time budget of its own, and claims
  through the same reservation as "fire now". Every condition is read again inside the claim: enabled, paused, open,
  the tenant switch, the usage limit, the count of the day, the back-off. Members are served by their oldest waiting
  message, one fire per member per round.
- **When a queue is open.** A window is a cron expression for its start, a length in minutes, and a time zone kept
  with the queue: the Control Plane does not know a member's zone, so the Console sends the browser's. A threshold
  waits a minute with nothing added and then drains, as §6 says; that it is draining is stored in the row, a pause
  keeps it, and disabling the queue clears it. A window and a threshold together are refused.
- **Back-off.** A delivery that was provably not sent returns its messages, and the queue then waits: five minutes,
  doubling to four hours. §6 says "the queue backs off" without a number.
- **The breaker (§7).** It looks at the two newest deliveries whose first turn is recorded, counted since the last
  resume. A first turn that ends after its delivery closed is still collected, for a day. A pause stops runs that
  start by themselves and leaves "fire now" working.
- **The usage limit (§7).** Kept per delivery as the agent kinds of the limited sessions in its tree, and compared
  with the queue's kind at the claim.
- **Wakes made only to settle.** As §6 says for a `sending` delivery, and also for an open delivery that is overdue
  and not yet retired, when such deliveries hold every slot of a queue that would otherwise fire. At most two per
  tick and one per member in ten minutes, each recorded as a closed delivery row of its own outcome so that it
  counts toward the day. A queue whose workspace is already recorded as having an Agent that does not report is not
  woken for this; a workspace not yet read can be, and the check after the start then records it.
- **Overdue.** As §6 says (12 hours by default, retire only after an hour with no progress anywhere in the tree).
  New: the bound is a setting per queue, `maxTaskHours`, 1 to 72; it counts from when the delivery opened; the
  member is told once.
- **Notifications.** The four of §7 (paused, every slot waiting for a person, unconfirmed, overdue). New: each is
  told once per occurrence, and "every slot waiting" once per set of deliveries.
- **Words on screen.** Auto-run runs "at any time", "in a time window" or "once notes are waiting". A run that is
  still open says why: working, waiting for you, waiting for a usage limit, waiting on a watch or a timer, not known.
  Overdue is 期限超過 in Japanese (the owner's choice), and the button is 今すぐ実行, as for schedules. An approval
  card marks a note that changed while it was on screen and asks for a confirming press before it can be approved;
  the Control Plane's rule (the press is bound to the content shown) was already enough for safety, the mark is for
  the person.

Left out of step 2 and tracked: a retire conditional on what the Control Plane read (#4218); first-turn records left
by a create that failed (#4210); the order of the settle and retire passes when workspaces are slow (#4245). Open
questions 1, 4 and 5 stay with #4149: the defaults have not been measured on a deployment.

Added 2026-11-16 (#4286, seen on a deployment): an answer to a carried question from the Console counts as a person's
start of a retired session. It records the act, launches, takes the retired mark off after its own launch and then
delivers the answer, as `/start` does; the two switches still refuse a marked session.
Added 2026-11-16: an overdue run whose stopped session carries a question, a plan or a permission stays open, as the
second row of the §6 table says; the answer from the stopped session's card clears the wait without a Start first, and the run ends once the work the
answer resumed is done.
Added 2026-11-16 (#4291): the same holds once such a session is started again without the answer. A live row of the Agent's
list carries the kind of the carried interaction under its own field, `carriedLive` (`carried` stays on stopped rows only),
and the Control Plane counts it as a person's hold, below a machine at work, until the interaction is answered, discarded or
expired.
Added 2026-11-16 (#4249, decided by the owner): what starts a retired session again, and clears its mark, is a deliberate
act: the member's own (Start, a carried answer, a message typed to a Managed session) or one of the member's own sessions'
or connections' (a peer or operator message, `resume_session`). What fires by itself never does (a schedule, a watch or
timer notice, a booked usage-limit resume, pull request feedback, a plan-review reviewer): it is refused with 409
`session_retired` and the mark stays.
The Control Plane's scheduler says its start is automatic (`?origin=automatic`), so the Agent records no act for it and the
schedule's run records the refusal as its failure; a caller that says nothing is deliberate, so an older Control Plane keeps
working. The same rule holds in both execution methods: a deliberate message to a retired, stopped Managed session starts it in
`/start`'s order (act, launch, mark off after its own launch), an automatic one is refused.

Added 2026-11-16 (#4315): a wake made only to settle is also written to the audit log, once, when it is recorded: action
`workspace.queue_settle_wake`, actor the system, the member's tenant, the workspace as target, and the category and the
runs (`sending`, or overdue and not yet retired) that made it necessary. No run history line and no change to the
deliveries list; no memo or instruction text is kept.

Added 2026-11-16 (#4245): the settle pass and the retire pass are fair across members. Deliveries that need no workspace
read are settled first, whatever a workspace does: a stale claim is closed, and the sends are taken oldest first up to
their per-tick cap, so the order of sends does not depend on which member is slow. The deliveries that need a read are
then taken member by member, the member tried longest ago first, with each member's own deliveries oldest first, and the
pass stamps a member before it tries it. The retire pass keeps its own order, apart from the settle pass's. The two share
the one 60 second budget as before: the read pass stops 20 seconds early so the retire pass always has that much (and
whatever the read pass left over), and the time before the tick's fires start does not grow. What this guarantees is an
order, not a fixed latency: while the set of members with open work stays the same, and each tick can try at least one
member, every member is tried within as many ticks as there are members. How many ticks that really is depends on the
workspaces: one list read of a silent Agent costs up to 15 seconds, so several silent members ahead of a good one can use
a tick's budget between them, and a member whose retire calls each wait 30 seconds can use a pass's budget alone (the tests use one silent member and a budget that one read exhausts, hence two ticks). A member that leaves the set is
forgotten and comes back first in line, so no bound is promised for a changing set. The order is remembered in the
process only: each Control Plane is fair by itself, and nothing depends on two of them agreeing. A member the settle
pass did not reach has no view in the tick, and the dispatcher reads it itself, once.

Added 2026-11-16 (#4210): a create that fails to start its session now removes the first-turn record it armed, and
the Agent removes at start the records no session meta claims (armed more than a day ago; the round is skipped when
a meta cannot be read). Nothing about what the record holds or when it settles changed. The retire's walk reporting
a child create still in flight in `unended` is covered by the Control Plane repeating a retire that is not complete.

Added 2026-11-16 (#4269): the retire's reads of a session's meta no longer take a file that exists but cannot be read for
"absent". Such a file in the walk's list is named in the root's `unended` (never `complete`), a retire of a root with one
is `503 meta_unreadable`, an automatic start of it is refused with `409 session_retired`, and the act record is an error.
The meta write itself is now a temporary file and a rename, so a reader no longer meets a cut file in normal operation.
A meta that stays unreadable would keep every retire from completing, so the Agent sets such a file aside at start
(renamed `<name>.json.broken-<unix seconds>`, never deleted) when no tmux session of that name exists; one whose session
exists stays in place and in doubt. Renaming it back after a repair makes it a session again, with no retired mark.
The set-aside is skipped while an adoptable Managed runtime (opencode serve, the codex app-server) may be up from the previous Agent, since a running turn there has no tmux session.

Added 2026-11-16 (#4265): the act mark comes with an incarnation of the Agent's counter (`actIncarnation`, made at each
load of the counter and not kept in the state file, so a restored older copy cannot carry it). The Control Plane keeps it
with the mark (`memo_delivery.act_incarnation`, `retire_act_incarnation`; migration 0137, Postgres 0122) and sends both
with the retire, and an act records it beside its mark (`personActIncarnation`). Nothing is refused: the Agent judges each
session. An act recorded in the current run is after any list of an earlier run (released whatever the numbers say), which
closes the restored-state-file case of the note of 2026-11-15 and also keeps a session a person started after a restart
while its retire was pending. The remaining gap needs two restarts with the retire pending and a rolled-back counter. An
older Agent sends no identity and an older Control Plane sends none: both behave as before.

Added 2026-11-16 (#4175): §2's mapping is built, and "filing a note added from a worktree under its parent repository" in the
list of what step 1 left out is done. Decided by the owner: it applies to every note a session adds or moves, plain notes
included, not only to categories with auto-run on; a worktree folder is always filed under its parent repository. The Agent
maps `repo` before it relays a create, and an update that sets `repo`: while the folder exists under `~/repos`, git decides
(a linked worktree maps to the folder of its main working copy, a worktree of a worktree included, and a folder that is not
a linked worktree is left alone even with an "@" in its name); once the folder is gone, the name rule of the Console applies
(everything before the first "@"). A name that is not one folder name, or a git call that fails or times out, is relayed as
named: the memo is never refused for the mapping. The Console files what it derives from a working copy under the parent too
(the send dialog of a file). What a member chooses by hand stays as chosen: the composer inside a group and a drag onto a
group keep that group's repo, a `repo@slug` one included. Memos and categories already under a `repo@slug` bucket stay where
they are: no migration and no rewrite on read, and a member can drag them. The launch dialog's stashed draft keeps its key
and its memo's repo: it is saved per working copy on purpose. The normalised `host/owner/repo` of §2 stays unbuilt.

Added 2026-11-17 (#4150, slice A of step 3, the Control Plane): a message may carry a reference `{provider, key, url}`
(`memo.ref_*`, migrations 0138 and 0123), on `/api/memos` and `/internal/memos`. The release hash covers it, so a change
of it from the token face or `/mcp` puts a released message back to `held`. A queue holds one open message per
(provider, key): open is `held`, `pending` and `claimed`, closed is `unconfirmed`, `delivered` and `superseded`.
Adding one that is open answers the existing message (`existing: true`); an update, a move into the queue, a category
that becomes a queue and a merge that would make two refused whole (`memo_ref_duplicate`). The check is in the
member's lock, with a partial unique index behind it. A fire passes the reference with the item and, for exactly one
reference, the session's `work_item`. A delivery that holds a reference is not sent to an Agent that does not send the
new `queueRefs` marker (the queue's `noRefsAt` and the waiting reason `no_references` say so). Open question 2 is
untouched: a reference-only message from a session is `held` like any other. The §4 sentence on the branch name is not met by this slice: the queue's create sends no branch, so the session keeps the Agent's `temp/` branch and only the link follows the work-item rules. Naming the branch from the item is for slice B. Left for the Agent (the marker, the prompt
and the tool argument) and the Console ("add to auto-run"); the wire is in docs/build/03-control-plane.md §3.6.

Added 2026-11-17 (#4150, slice B of step 3, the Agent and the hosted tools): `add_memo` and `update_memo` take an optional
`reference` `{provider, key, url}`, on the Agent's session tools, through the relay (absent keeps it, `{}` clears it, a full
object sets it) and on the Control Plane's hosted `/mcp` tools, which enter `held` as every `/mcp` write does. A call that
sent a reference and got an answer without one says the reference was not kept (an older Control Plane ignores the field).
A delivery's prompt shows an item's reference as quoted data beside the member's note, never renders a reference-only
item empty, and says once in Agent Fleet's own words that nothing fetched the item and the session reads it itself. The
Agent sends `queueRefs: 1` with that. The §4 sentence on the branch is now met: a queue create with a work item, a
worktree and no branch takes the branch the member's naming rules give for the item (the default is `feature/<number>`
for GitHub and Bitbucket and `feature/PROJ-7` for Jira, with the rules' base). An existing branch is checked out, as the
Console's "work on the existing branch" does, and a branch another working copy holds, a name git refuses or no name from
the rules leaves the throwaway `temp/` branch: a name never fails a delivery. Wire and behaviour: docs/build/04-agent.md.

Added 2026-11-17 (#4151, step 4): the switch is `memo_queue.accept_own_sessions` (off by default, set by the Console's
settings PUT only; an omitted field means off). With it on, a `workspace` message whose recorded producer origin is
exactly `user` enters `pending`, with its release recorded as a press records it; every other origin, an empty one
and `/mcp` stay `held`. A change that arrives by the token face to such a message keeps it `pending` and records the
release again: the update carries no caller identity, so the Control Plane can only read the origin recorded at the
create, and the switch already accepts that a session of that origin could have added the changed text as a new
message. A move into a category without the switch, and a change by `/mcp`, put it back to `held`. A message the switch
made pending carries `memo.released_by = 'setting'` (`releasedBy` on the wire), which the Console draws as
"Accepted without approval"; a press or a Console write clears it. The bounds on a create from a session (rate, size, identical
message, the envelope prefix) and a queue's daily cap and breaker are unchanged. The Agent already relays the session's
origin (`session.OriginOf`); nothing in the workspace changed.

A change of the switch does not touch the rows already there: turning it off leaves an accepted message `pending` and
marked (a release is one-way, as a press is), and turning it on releases no `held` one. A plain note outside the queue
that a session of origin `user` moves into a queue with the switch on is treated as entering and becomes `pending`; a
`held` message that moves keeps its state. A press on a message that is already `pending` changes nothing, so it keeps
the mark. During a rolling update an older Control Plane that presses such a message leaves `released_by = 'setting'`
on it until the newer one writes the row again: a display slip only, the state and the release copies are the press's.
The origin is compared after the relay's value is trimmed, so `" user "` is `user`.

Added 2026-11-17 (#4153, open question 3, decided by the owner): neither, for now. No pull tool is built, and an existing
session does not become a queue's consumer. A queue keeps delivering only by starting a new session per fire (§6). A
person who wants a session they are driving to work on waiting messages ticks them in the memo queue and sends them to
that running session with "Send…" (the Send memos dialog, `console/src/features/memo/SendMemoModal.tsx`; guide/user/chat/memos.md,
"Send them together"), as today. That already covers the case, and a pull tool would add a state and a claim to a message's
life for what is another way to ask for the same thing. The three reasons under "Rejected" ("Delivering into an existing
session") still hold. Either option may be wanted later, so what each has to settle is recorded here. Nothing below is
decided, and the options are not ranked.

Option A, a pull tool: a tool with which a session a person is driving takes the next N messages of a queue. If only a
session a person is driving could take, nothing would be injected unattended and the provenance rules of §3 would not
widen. Whether that "if" can hold is the question below. To settle before it is built:

- *Which messages may be taken.* A `held` message has not had its press, and §3 makes the press the only wall. A tool is
  offered to every session, ones nobody is driving included ("Letting sessions enqueue straight into automatic fires").
  The Control Plane cannot verify which session calls (fact 1), and the origin the Agent stamps is a guard rail, not a
  wall (§3). So: may a take reach `held` messages at all, and what says that a person is driving the taker?
- *The claim.* The ledger of §6 records one delivery per fire, bound to the session it creates through the durable key
  `q_<delivery>`. A take needs a record that names the taking session, and the move out of `pending` has to be as
  conditional as a fire's claim, so that the same message is not also fired.
- *What a taken message counts as.* When it is closed; whether it holds one of the queue's `max_active` places (§6: every
  delivery not closed does) and counts toward the daily cap and the breaker (§7, which reads the first turn of a
  delivery's session); and what becomes of it when the session never acts on it.
- *The standing instruction and the data rules.* Whether the taker receives the queue's standing instruction, and
  whether the messages reach it as data with an origin line under §5's rules (the four prohibitions of a peer message).

Option B, an existing session as a queue's consumer. Each reason would need an answer:

- *Idempotency per fire.* The durable key of §6 covers the create of a session: `POST /sessions` with
  `idempotency_durable`, a record on disk, and reserve, abandon and lookup answers that reconciliation acts on
  (`workspace/agent/internal/sessionx/session_idempotency.go`). Input to an existing session has no such key. The only
  de-duplication there is a Managed driver's per-session ledger of the last 200 client message ids
  (`workspace/agent/internal/agents/msgledger.go`, used by `workspace/agent/internal/agents/turnqueue.go`): it drops a resend, is not tied to a delivery, and cannot answer
  "was this input taken" the way abandon does. A Terminal session has no such ledger.
- *Knowing that the session took the input.* A Terminal session's input queue is invisible to Agent Fleet. The ADR says
  nothing different of a Managed session; the Agent has a turn queue for one, and whether the Control Plane can read it
  to settle a delivery is not decided here.
- *A bound on the consumer's context* (ADR 0021 decision 3).
- What else assumes one session per fire: retire (§6) ends the watches and timers of the session a delivery started and
  of the sessions it started, cancels their booked resumes, and stops them, which cannot be done to a session that is the person's own; the worktree
  is new per fire because two sessions in one working copy corrupt each other; §3 gives no standing permission for unattended
  input into a driven session at all, since its press-or-switch rule is about causing an unattended run, not about
  feeding one; and how pushed input is marked so it does not read as the member typing (fact 2).

What reopens it is a real, recurring case that sending a selection by hand does not cover; a new issue is opened then.
#4153 is closed by this note.

Added 2026-11-17 (#4152, step 5, slice S1, the Control Plane's core): the schema (migrations 0142 and 0127), the settings
`tidy`, `tidyDestinations` and the read-only `noTidyAt`, the validator (`control-plane/internal/memotidy`), apply, undo and
the run list are built, and inert: nothing starts a run and the dispatcher does not read `tidy`. The Console's wire and the
codes are in docs/build/03-control-plane.md. Decisions taken for the build: a derived message takes the state of its sources
(§3, the `curator` row), so tidying `pending` messages gives `pending` results with no press; a message the manual path
tidies while `held` gives `held` ones, and a move never releases a held message. The tidy switch is a guard rail, not a
wall, exactly as "accept from sessions I started" is (§3, fact 1): the model's reply is produced inside the workspace, so
with the switch on, code there that can answer for the Agent can choose the text of a derived message. What bounds that is
checked by the Control Plane and not by the Agent: the operations are over the input ids only, a merge needs one
(provenance, producer, producer origin) group and plain text notes, a text is at most 16 KiB and at most 1 KiB longer than
its sources, a move is decided from the stored settings at apply time and never from the reply (so a reply cannot send a
message into a queue the person did not list as pending), the whole reply is dropped on one violation, and a run is undone
in one press until a result is claimed. What it can do is put words the person did not write into a message that then fires, which is why the switch is off
by default and says so.
A superseded row, which keeps `sent_at` so the existing sweep collects it, never becomes `held` or `pending` except by undo
on any write path, and an older binary turns one into a sent note or a `delivered` message at worst.

Added 2026-11-17 (#4152, step 5, slice S3, the dispatcher): the automatic run is built and stays inert until the Console can
set `tidy`. The built order is claim, then wake, so "after the wake and before the claim" became a stage of its own inside
the fire's transaction: after the queue may fire and before the claim, a queue with `tidy` on and a message no run has looked
at reserves a run instead of claiming, and the claim is a later tick's; the job wakes the workspace, calls the Agent and
hands the reply to apply, and every failure, skip and refusal marks the input seen, so the messages fire as they are. "Run
now" never tidies. One run at a time per member holds across Control Plane processes (a unique index; a queue meets it as the
waiting reason `tidying`), a run past 10 minutes is ended by the next tick (`takeover`) and its late apply writes nothing, at
most 2 runs are reserved per tick, and the daily cap of 10 is counted behind the member's lock. A `curator` message is
sendable only to an Agent that shows `queueTidy`: by itself the claim leaves it pending (`no_tidy_at`, the waiting reason
`no_tidy`), and the send checks the marker after its wake. A threshold queue is made draining by the reservation, so a merge
that leaves fewer than its threshold still fires. The codes, the bounds and what clears `no_tidy_at` are in
docs/build/03-control-plane.md.

Added 2026-11-17 (#4152, step 5, slice S4, the Console): the switch that turns tidying on and everything a member sees of it.
The settings dialog has the switch (off by default; its hint says that a model inside the workspace rewrites the waiting
notes, that code there that can answer for the Agent could put words the member did not write into a note that then runs, and
to turn it on only for a workspace they trust) and, with it on, the choice of the other auto-run categories a move may land in
as pending. The PUT replaces the row, so `tidy` and `tidyDestinations` are always sent with the form's value, the list also
while the switch is off. A category's notices say `tidying` as a waiting reason and say `no_tidy` and `noTidyAt` once, with
what to do (a workspace that can run Claude, or the switch off and the run undone), whether or not the switch is still on,
because the derived notes wait either way. The memo list marks a derived message (made from N notes, of that origin), a
moved one (from the category, or without a name when it is gone), the pending ones a run released (next to "accepted without
approval"), and a replaced one, which is read-only: no edit or send, no select box, and a link that opens the run history
with that run marked; a hand delete stays, as it does for a sent note, and undo then says `gone`. The release card of a
category shows where a held message a tidy moved came from. The tidy runs are a part of the run history dialog, below the
runs: state and reason in words, counts, `usedToday` of `dailyCap`, and an undo button where the Control Plane says the run
is undoable; a refused undo (409 `memo_tidy_not_undoable`) names the blocked messages by reason, and `later_run` says to undo
the newer run first. Undo is one press with no confirmation, since it puts back what the run took. An older Console would
drop the two new waiting reasons, as it drops any it does not know.
