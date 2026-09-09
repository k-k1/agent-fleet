# 0073. A session may steer only the sessions it started itself, and they are recorded under a new origin, `session`

English | [日本語](0073-session-spawned-sessions.ja.md)

- Status: **accepted and implemented** (2026-09-09; the implementation record is
  [87-session-spawn.md](../log/87-session-spawn.md), and the four follow-ups are
  [89-child-session-listing.md](../log/89-child-session-listing.md); amendments: `list_child_sessions`
  in §3-b, archiving in decision 6, the completion timestamp in decision 9). The design took two rounds of review by
  another session and the implementation a third (round 1: decisions 1, 4, 5, 6, 7, 10 and 11
  corrected, decision 14 and §3-b added; round 2: archiving and reservation in decision 6, the
  comparison unit in decision 7, splitting the two surfaces in decision 14, the rejection
  reasoning in decision 5, the test policy in §3-b; implementation review: decision 6's predicate,
  which value decision 7 compares, and the order the refusals run in)
- Related: [86-session-fleet-observe.md](../log/86-session-fleet-observe.md) (stage 1 — the test
  in §86.2, the leftovers in §86.9) /
  [0041-cross-session-messaging.md](0041-cross-session-messaging.md) (the additive-flag shape,
  decision 4 on leaving the arm alone, decision 5 on excluding shell, decision 6 on the envelope,
  decision 13 on intent) / [0029-usage-accounting.md](0029-usage-accounting.md) §6 (the provenance
  axis) / [46-usage-accounting.md](../log/46-usage-accounting.md) §2-c /
  [51-session-report-v2-ledger.md](../log/51-session-report-v2-ledger.md) (who owns the ledger
  and the arm) / [0056-tool-permission-choice.md](0056-tool-permission-choice.md) decision 1 (a
  session runs with permission prompts skipped by default) /
  [0069-image-generation-providers.md](0069-image-generation-providers.md) (the progress
  heartbeat and the measured per-kind ceilings) /
  [44-operator-interaction-graph.md](../log/44-operator-interaction-graph.md)

## Context

Stage 1 (docs/log/86) opened four observation tools to the session surface. What is left of the
request is **session steering**: the group around `create_session`.

The reason it cannot simply be handed over is not the individual danger of any one tool. It is
that the **three properties a session structurally lacks** (§86.2) are baked into
`create_session`'s own plumbing.

- `create_session` today stamps `origin=operator` and `origin_conv=convID()`. Called from a
  session that is **a lie — "an operator started this"** — and it breaks the axis ADR 0029 §6
  exists for: separating unattended spend from sessions a human opened.
- The idempotency key `CreateSessionKey(convID(), …)` includes the conversation. A session has
  none, so the first argument is empty and **two different sessions launching the same thing
  collapse onto one**.
- `report_to` is a conversation too, so it is empty as well: **a child's completion reaches
  nobody**. And with `report_to` empty the initial prompt is not recorded as an injection at all
  (`session_handlers.go:805,833`), while an empty `Source` reads as the user's own input
  (`session_injections.go:24`) — so **from inside the child, an instruction from its parent is
  indistinguishable from one its user typed**.
- `BridgeApprovalGate` (the approval on shell targets) is a no-op without a conv, so the gate is
  the same as not existing. A session additionally runs with permission prompts skipped
  (ADR 0056 decision 1).

`create_session` also has two properties no other tool here has: it **recurses** (a child can
start its own children) and it **actually consumes the shared, memory-constrained host**. Absent
limits designed in from the start, it must not be opened at all.

### Terminology: "handoff" names two different routes

This ADR always says which one it means. Conflating them collapses the reasoning behind
decisions 1, 4 and 5.

| Name | What it is | Origin |
|---|---|---|
| **fork (formerly "handoff")** | `HandleForkSession` (`session_handlers.go:964`) — branches directly off an existing session | `origin=handoff`, inherits `origin_conv` |
| **handoff proposal** | `propose_session_handoff`. **It starts nothing.** The user reviews it in the Console and launches it through the ordinary flow (`HandoffProposal.tsx:212` → `StartHost.tsx:94` → `useStartWork.ts:40,70` → an ordinary `POST /sessions`) | the source session's provenance is not passed, so `origin=user` (`session_handlers.go:467`) |

## Decision

### 1. Add a new origin, `session`, and an `origin_session` field

Add `OriginSession` (wire `origin_session`) to `session.Meta` and `session` to the origin
enum — an amendment to the frozen table in ADR 0029 §6, landing in the same commit as the code.

- **The server fills `origin_session` in, not the caller.** It resolves from the MCP server's own
  `AF_SESSION_NAME` (container env, not something the model can write), exactly as `peer_from`
  is handled. A value arriving on the wire is not trusted.
- `origin_conv` **stays empty**. Reusing it for the parent session name would need no new field
  and would put a lie into a frozen accounting dimension.
- **Only `origin` is baked into usage rows**; `origin_session` stays on the Meta. The axis
  ADR 0029 §6 needs is "unattended or human-opened", and `session` answers it. **Lineage is a
  question for the ledger and the fleet overview** (ADR 0041 decision 9 / docs/44).
- Only **two routes inherit it: recreate (`session_handlers.go:1251`) and fork (`:964`)**. Both
  already inherit `origin_conv`, and neither "the same slot again" nor "branched from there"
  changes where the work came from.
- **A handoff proposal does not inherit** (see the terminology table). Once the user launches it
  from the Console it is `origin=user`, and that is **correct** — it is a human-opened session.
  What follows from the lineage ending there is decision 5.

### 2. Generalize the idempotency key's namespace from the conversation to the caller

`CreateSessionKey`'s first argument becomes a scope string: the operator passes its conversation,
a session passes its own session name. **Building a key from an empty scope is refused** — let it
through and two sessions that launched the same thing collapse into one.

### 3. An independent `--fleet-spawn` flag, off by default

A conjunction with `--self-report`, the same additive pattern as `--chromium-attach` /
`--peer-messaging` / `--image-gen` / `--fleet-observe` (ADR 0041 decision 3). The ui-prefs key is
`sessionFleetSpawn`.

It does not ride on `--fleet-observe`. Stage 1's own description promises the user that
**"nothing that acts is added"**; piggybacking would silently withdraw that promise.

~~It does, however, **require fleet observation in the Console**~~ — the prerequisite itself is
gone (2026-09-09). It existed because `get_session_status` is the only way to watch a child, so
spawning with observation off would create sessions that could be started and then never looked
at again. **Observation then stopped being a setting at all**, and every session has
`get_session_status` (see the amendment in docs/log/86), so the prerequisite is permanently
satisfied and was removed from the UI and from `uiprefs.FleetSpawn()`. What the decision was
protecting — never being able to start something you cannot watch — holds more strongly than
before.

### 3-b. The nine tools stage 2 advertises (the whole set, by flag)

| Tool | Advertised by | Extra gate |
|---|---|---|
| `create_session` | `--fleet-spawn` | decisions 5, 6, 7, 8 (the pre-launch refusals) |
| `list_child_sessions` | `--fleet-spawn` | decision 4's predicate, as the row filter |
| `list_repos` / `list_models` / `get_agent_usage` | `--fleet-spawn` | none (reads for choosing where and which agent) |
| `get_session_output` | `--fleet-spawn` | decision 4 (children only) |
| `stop_session` / `stop_session_after_turn` / `resume_session` | `--fleet-spawn` | decision 4 (children only), decision 10 |

**Amendment (2026-09-09, docs/log/89): `list_child_sessions` is the ninth.** Stage 2 shipped
with eight, and every tool that names a target took a name the caller could only have got from
one `create_session` result — so a compaction left the parent unable to name its own children,
and the instruction added with it ("before your last turn, name the children you left") became
impossible to carry out. It reads `GET /sessions`, which already computes live state per kind
and already drops archived rows and prunes expired stopped ones, and filters it by decision 4's
predicate. It is the only one of the nine that is not also an operator tool (the operator has
`list_my_sessions`), so it is also the only one whose call-side gate is the flag alone.

- Stage 1's four (`get_session_status` / `get_session_usage` / `list_memos` / `add_memo`) stay on
  `--fleet-observe` unchanged.
- **Remove nothing from `TestFleetObserveDoesNotOpenOperatorTools`'s list**
  (`mcp_stdio_test.go:792`). That test looks at what observation advertises **on its own**, and
  `create_session` / `stop_session` / `resume_session` / `get_session_output` standing in that
  list is exactly the property stage 2 wants pinned (without `--fleet-spawn` they do not appear).
  Taking the eight out of the list is taking the property out of the test.
- **Add a test instead pinning that raising `--fleet-spawn` adds exactly those nine.** The pair
  keeps both halves: "observation alone does not open them" and "adding spawning opens these eight
  and nothing else".

### 4. A session may steer only the children it started

`get_session_output` / `stop_session` / `stop_session_after_turn` / `resume_session` pass only
when the target's Meta has **`origin == "session"` and `origin_session == the caller`**. The check
lives in a handler-side gate shaped like stage 1's `memoWriteAllowed()`
(`sessionDriveAllowed(target)`). The advertised tool set is the first boundary; this is the
second.

Both conditions are required so that **a fork successor (`origin=handoff`) is not a child**: a
fork is something a person performs in the Console, and there is no reason a parent should steer
it.

### 5. Depth is one generation *between human launches*

**A session whose `origin_session` is non-empty may not call `create_session`.** It takes a
single lookup of the caller's own Meta, walks no chain, and therefore does not break when a
session in the middle has been deleted.

The condition is "`origin_session` is non-empty" rather than "`origin == session`" because
forking a child produces `origin=handoff`, which would slip through. This is **deliberately a
different predicate from decision 4**: steering is narrow, recursion suppression is wide.

**"Grandchildren are impossible" would be false.** A child can call
`propose_session_handoff`, and once the user launches it from the Console the successor is
`origin=user` with an empty `origin_session` (see the terminology table) and may spawn again.

This is not impossible to close — it is **chosen not to be closed**. The two fields are
independent, so keeping `origin=user` (a human-opened session, which is what accounting needs)
while inheriting only `origin_session` is technically available and breaks no aggregate. It is
not taken because it would **strip a session the user explicitly launched of the ability to
spawn, merely because the proposal happened to come from a child** — and it would need lineage
plumbed through the proposal store and the Console launch flow.

So what this decision guarantees is not a tree depth but that **between one human launch and the
next, sessions alone can extend the chain by one**. Nothing grows without a person in the loop.

### 6. At most three children per caller — a budget counting stopped children, but not archived ones

Count the children the caller **created** — `origin=session` with `origin_session` equal to the
caller — **whose Meta still exists and is not archived**, and refuse at three. Not `origin_session` alone: that would
also count a fork of a child (lineage kept, `origin=handoff`), and a fork is something a person
does in the Console, so charging it to the parent would let the user's own fork be the reason the
parent may not spawn. Nothing escapes there — a fork of a child cannot spawn either, because
decision 5's predicate reads the lineage, deliberately the wider one.

- ~~**Only deletion (`RemoveMeta`) frees a slot.** Archiving keeps the Meta and merely hides it
  from the active list (the `Archived` flag, `session_handlers.go:134`), and **there is a restore
  route**. Let archiving free a slot and the limit is beaten by folding up, spawning, and
  restoring.~~ **Amended 2026-09-09 — see the amendment below.**
- Counting "live children" fails too. `resume_session` (`mcp_stdio.go:2300`) and the auto-resume
  behind a peer send (`:1789` → `agentResumeAndSend:3246`) both hit `/start` directly and so
  **increase the number of running children without going through `create_session`**. Counting
  until deletion puts the only way to grow the set back inside create.

**A recreate stays one slot.** Recreate keeps the old meta (archived, restorable) and mints a
new one, and both inherit the parent — so left alone **one child costs two slots**, and a user
recreating their own child is the reason the parent may not spawn. Same objection that keeps
forks out of the count, so the lineage is moved to the successor once it has launched
(`handOverSpawnLineage`). **The consequence**: the replaced identity stays in the archive with
`origin=session` and no lineage, so **if the user restores it, it may spawn**. Reaching that
takes two deliberate Console-only human actions — recreate, then restore — so decision 5's "one
generation between human launches" still holds. It is not closed off because closing it would
mean keeping a slot charged to a session the user deliberately replaced.

**The slot is reserved, not merely counted.** Running the count under the same lock as the create
idempotency ledger (`session_idempotency.go:48`) is not enough: that ledger serializes one
idempotency key, so two concurrent creates with different content run before either Meta is
written, both see "two existing", and four land. Increment a per-parent counter under the
ledger's `begin` lock **before launching, and roll it back on failure**. What is counted is
"existing child Metas plus this caller's in-flight creates".

**Three is provisional, not a measured resource limit.** Until measurement replaces it, the number
goes into the refusal text so it never becomes an invisible limit.

#### Amendment (2026-09-09, docs/log/89): archived children stop counting

The original rule counted archived children so that "fold up, spawn a replacement, restore"
could not beat the limit. What it missed is that **the slot never comes back**. The stopped-session
TTL prune (`session.StoppedTTL`, 7 days by default) explicitly skips archived metas
(`if m.Archived { continue }` in the sessions listing), so **an archived child holds its slot for
ever**. Measured in one real workspace: 209 archived sessions. Under that, a parent that archived
three children can never spawn again — **the user who tidies up is punished hardest**, which is the
opposite of what the rule was for.

The loophole it was defending against is not shaped like one. Archiving is **not open to a
session** (decision 13), and restoring means finding one row among a couple of hundred in the
Console. Both ends are a person's deliberate action, which is precisely the ground on which forks
are kept out of this count and on which a recreate hands the slot to its successor: a person's
action must not spend a session's budget.

**So a slot frees on deletion, on archiving, and — for a child left stopped — when `StoppedTTL`
expires and the listing prunes its meta.** "Only deletion frees a slot" was wrong about the TTL
even before this amendment, and the refusal text, the tool descriptions and the Console note said
so; they are corrected (docs/log/89 §89.5).

**`handOverSpawnLineage` (above) stays, with a different reason.** The double count it was written
for no longer happens while the predecessor is archived. What it still prevents is the double count
that would come back **when the user restores that predecessor**.

### 7. `worktree` defaults to true, and `worktree=false` is refused on another live session's directory

The operator surface defaults to `false` (a person can say "work right here"), but the session
surface defaults to `true`. Called with defaults otherwise, parent and child **share one working
copy between two agents** — precisely the accident the workspace policy forbids by name to every
session.

An explicit `worktree=false` is refused when another live session is working on the target.

- **What is compared is `dir`** — the working copy itself — and `subdir` takes no part in it: the
  same working copy is the same working copy whether the other session sits in `console/` or at
  the root.
- **The value compared is the RESOLVED `dir`**, the directory the session will actually run in.
  The create turns an empty dir into home and joins a relative one onto home, so **comparing the
  request as sent lets `dir: ""` walk past a session already running in home**. Symlinks are
  resolved too: letting two spellings of one directory read as different targets makes the check
  decorative.
- Stopped sessions do not count: what this guards is two processes running at once, not a quota
  (decision 6 has the other purpose).

### 8. `kind=shell` and `ssm` are refused

The reasoning of ADR 0041 decision 5. Launching a shell is arbitrary command execution, and a
session that has read a poisoned repository must not be able to run arbitrary commands elsewhere.
`BridgeApprovalGate` is a no-op without a conv, so **the approval gate that exists on the
operator surface does not exist on the session surface**.

### 9. Completion comes back by the parent polling, with the child's `intent=answer` envelope as an optional aid

- `report_to` **stays empty**. No session-addressed report channel is created — ADR 0041 rejected
  exactly that ("a report is addressed to a conversation, not to a session"), and it would break
  who owns the arm (docs/log/51).
- The primary route is **the parent polling `get_session_status`**. That is what decision 3's
  prerequisite is for.
- As an aid, **only when peer messaging is on**, `create_session` appends one line to
  `initial_prompt`: when you are done, send the parent (named) one `send_to_peer_session` with
  `intent=answer` (argument `report_back`, default on; nothing is appended when `initial_prompt`
  is empty).
- **The arm is never touched** (ADR 0041 decision 4). `answer` is a protocol terminal
  (decision 13), so the line cannot produce a reply loop.
- The server appends the wording rather than **asking the model to write it**, so whether the
  report arrives does not depend on the caller's prose.
- The provenance of that initial prompt, appended line included, is decision 14.

**Amendment (2026-09-09, docs/log/89): the polling route carries a completion timestamp.** The
weakness of "poll `get_session_status`" is that idle means both "finished" and "started and never
given anything", and a machine idle is not a semantic completion — a child goes idle at the end of
every turn (docs/log/51 exists because of that gap). `list_child_sessions` therefore carries
`lastTurnEndAt` per row: the moment that child's newest turn ACTUALLY ended, taken from the one bit
the status store raises only on a real end of turn (`status.SessionStatus.TurnEnd`) and never on an
idle nobody can explain. It is empty rather than guessed, so `idle` with a timestamp and `idle`
without one are finally different answers.

**A server-fired completion notification was considered and rejected** (docs/log/88 §88.6-1).
af's only route into a session is typing into its TUI, i.e. starting a turn — so it would save the
child one turn and unconditionally spend one of the parent's, and delivery resumes a stopped
parent, making "the parent wakes up every time a child finishes" the default. Detection is worth
having; delivery is not. A row on a list the parent already polls costs no envelope and no wake-up.

### 10. A session-issued `stop_session` does not send `disarm_report`

The operator's `stop_session` sends `disarm_report:true`, which means "the operator withdrew its
own instruction". **A parent folding up a child does not withdraw the operator's instruction.**
Omitted, the field defaults to false (`session_handlers.go:1064`), so the session surface simply
does not send it.

**"Does not change the arm" and "has no effect" are however different claims.** Stopping a session
puts an existing report for it on hold (`chat_report_reconcile.go:186,236`). If the operator had
given that child an instruction, the parent folding it up does not lose the report but **delays**
it. The claim is "it does not rewrite the arm", not "nothing happens from the operator's side".

### 11. `create_session` emits a progress heartbeat

`create_session` costs at worst 40 s (the POST) plus 45 s (waiting on the idempotency lookup)
(`agentCreateSession:3119`), which exceeds **opencode's 60 s per-call ceiling**. Reuse
`startProgressHeartbeat` (a `notifications/progress` every 10 s), added by ADR 0069.

There are two clocks to keep apart: **the Agent-side HTTP timeout** (`agentDo`'s default 15 s,
`mcp_stdio.go:3062`; 40 + 45 s for create alone) and **the ceiling a client allows one
`tools/call`**. The latter differs per kind and is measured (ADR 0069 §231): claude does not count
progress towards a timeout, codex is covered by the `tool_timeout_sec=600` stamped onto the af
builtin, and **opencode 1.18.29 cuts at 60 s but sends a `progressToken` and resets its clock on
every progress notification** (10 s intervals took a 90 s call through). So the heartbeat is not
"expected to work" — it is **measured to work**.

**`resume_session` needs no heartbeat.** It is a 15 s `AgentPOST /sessions/<n>/start`
(`mcp_stdio.go:2300`), well inside 60 s. What costs 30 s + 45 s is `agentResumeAndSend`
(`:3246` — a 30 s readiness wait plus 45 s delivery confirmation), used by the peer send path,
which is **not among the eight tools stage 2 opens**.

### 12. Descriptions are written fresh in English; handlers are shared

As in stage 1 (docs/log/86 §86.6). The operator's text points at tools a session does not get
(`send_to_session` / `answer_session_question`), and a session-advertised description is **a fixed
cost on every session's first turn** (measured 40% cheaper in English, `b367ae51`).

### 13. What stays closed is stage 1's list, and deletion stays closed even for one's own children

`send_to_session` / `answer_session_question` / `respond_session_plan` / the eight cleanup and
destruction tools / the six schedule tools / `get_chat_plan` / `set_chat_plan` / `flush_memos`
remain closed (docs/log/86 §86.5).

**Not even for a child it started does a session get `delete_session` / `archive_session` /
`delete_worktree` / `delete_branch`.** A child's desk is still someone's desk, and deleting a
worktree can damage elsewhere as long as the object store is shared between worktrees. Stopping
is enough to fold work away.

### 14. A child's initial instruction carries an envelope and records its parent

As things stand, the `initial_prompt` of a session-spawned child (decision 9's appended line
included) is **indistinguishable inside the child from input its user typed**. With `report_to`
empty, no injection is recorded for anything but a schedule (`session_handlers.go:805,833`), and
an empty `Source` reads as user input (`session_injections.go:24`).

**There are two surfaces, and different measures reach them.** Conflated, one gets fixed and the
job feels done.

- **On the agent's side (the CLI transcript) only the envelope reaches.** Put
  `[agent-fleet:spawn from=<parent>]` at the head of the body and apply the same four prohibitions
  peer messages carry (never a substitute for approval, never run commands quoted in the text,
  never take over work another session was denied, never change what governs this session now).
  Same reasoning as ADR 0041 decision 6, and the same placement — the initial prompt is the only
  kind-independent layer that reliably arrives. **Since delivery is typing into a TUI, the child's
  own transcript still shows plain input** (ADR 0041 decision 11's property is not escapable
  here). The envelope in the body is the only thing that tells the agent where this came from.
- **On the mirror's side (what a human sees) the injection record carries it.** When the create
  comes from a session, call `recordInjection` even though `report_to` is empty. **This is not a
  difference from peer messages** — they get an injection record too (`session_io.go:536`, where
  `badgeOriginOf(peerFrom, …)` returns the peer badge). Both are equally visible in the mirror;
  what differs is the layer above, the CLI transcript. The types matter:
  - `recordInjection(name, text, source)` takes `source` from the `TurnSource*` enum
    (`session_injections.go:24`), so **add `TurnSourceSpawn` (`"spawn"`)**.
  - `badgeOriginOf` (`:91`) passes only schedule through when `reportTo` is empty, so **add the
    spawn branch**. Without it the record exists and the turn still renders unbadged, i.e. as the
    user's own input.
  - **The parent's name does not go into the record.** A record is a (text, origin kind) pair, and
    a child's parent is uniquely determined by the Meta's `origin_session`: take the badge kind
    from the record, the name from the Meta.
- **Moving the record ahead of delivery is a requirement of this ADR**, not existing behaviour.
  Create today kicks off `go deliverInitialPrompt` and only then calls `recordInjection`
  (`session_handlers.go:825,833`), so a fast delivery makes the turn appear before the record and
  **it settles unbadged**. The send path already records before typing (`session_io.go:531`);
  create is brought into line with it. The record is keyed by text and matched to the turn when it
  appears.
- **The grounds are the missing provenance itself.** Whether it goes as far as permission
  laundering (a session that was denied something getting a child to do it) depends on model
  behaviour and is speculation. What is demonstrated is that the provenance is lost, and that is
  reason enough to close it.

## Rejected alternatives

- **Let `report_to` carry a session name.** ADR 0041 already rejected this. A report is addressed
  to a conversation; a session-addressed channel gives the arm two owners.
- **Reuse `origin_conv` for the parent session name.** It saves a field and puts a lie into a
  frozen dimension: `by=origin_conv` would then mix conversation names with session names.
- **Inherit only `origin_session` through a handoff proposal, closing grandchildren completely.**
  `origin=user` can be kept, so no aggregate breaks and **it does work technically**. It is not
  taken because it strips a session the user explicitly launched of the ability to spawn merely
  because the proposal came from a child, and it needs lineage plumbed through the proposal store
  and the Console launch flow (decision 5).
- **Count the limit over live children / let archiving free a slot.** The first fails because
  `resume_session` and the peer auto-resume add running children without going through create; the
  second is beaten by folding up, spawning and restoring (decision 6).
- **Count just before the create instead of reserving.** The idempotency ledger serializes one key
  only, so concurrent creates with different content cannot see each other and overshoot
  (decision 6).
- **Ride on `--fleet-observe`.** One switch, and decision 3's prerequisite is satisfied for free —
  but users who already turned it on **grow the right to start sessions with no notice**.
- **Derive the depth and count limits from measured resources.** An invisible limit gives no
  reason when you hit it. Refuse on a fixed number and put the number in the refusal.
- **Put an approval gate on the launch.** Without a conv `BridgeApprovalGate` is a no-op, so it
  only feels like a gate — the same shape of failure ADR 0041's addendum recorded for the env
  block.
- **Inject the child's completion straight into the parent's transcript.** The only injection
  route is typing into a TUI, and the receiving side cannot tell it from ordinary input
  (ADR 0041 decision 11). A peer envelope at least carries its provenance in the text.

## Consequences / open

- **`session` has to be added to the origin in four places** (in the same commit as the code):
  ADR 0029 §6's frozen table, `session.ValidOrigin` (`session.go:59` — miss this one and `session`
  silently degrades to `user`), the Console's frozen order table (`usage/colors.ts:86`), and the
  ja/en usage labels (`locales/ja/usage.ts:127` / `locales/en/usage.ts:128`).
- **Lineage through the Meta lasts only as long as the Meta does.** `RemoveMeta` (the delete route,
  `session_handlers.go:1035`) takes `origin_session` with it and the parent is unrecoverable. The
  `origin` baked into usage rows survives, so **"this was unattended spend" remains and "whose
  child it was" is gone**. Durable lineage would need another home (a ledger); this ADR does not
  take that on.
- **The docs/44 overview can draw lineage by reading `origin_session`** (within that retention).
  Adding `kind:"spawn"` to ADR 0041 decision 9's `DispatchEntry` is not taken up.
- As in stage 1, **firing has not been confirmed on a real session**. The descriptions are written
  to specify *when* to call, so it is worth confirming a real session calls them.
- Cleaning up children that outlive their parent stays with the user (decision 13). Decision 6's
  budget is a count over a caller's children, not a reservation, so **when the parent goes, so does
  the limit**.
