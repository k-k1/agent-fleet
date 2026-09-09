# 0073. A session may steer only the sessions it started itself, and they are recorded under a new origin, `session`

English | [日本語](0073-session-spawned-sessions.ja.md)

- Status: accepted, not implemented (the stage 2 design; implementation follows agreement on
  this ADR)
- Related: [86-session-fleet-observe.md](../log/86-session-fleet-observe.md) (stage 1 — the test
  in §86.2, the leftovers in §86.9) /
  [0041-cross-session-messaging.md](0041-cross-session-messaging.md) (the additive-flag shape,
  decision 4 on leaving the arm alone, decision 5 on excluding shell, decision 13 on intent) /
  [0029-usage-accounting.md](0029-usage-accounting.md) §6 (the provenance axis) /
  [46-usage-accounting.md](../log/46-usage-accounting.md) §2-c /
  [51-session-report-v2-ledger.md](../log/51-session-report-v2-ledger.md) (who owns the ledger
  and the arm) / [0056-tool-permission-choice.md](0056-tool-permission-choice.md) decision 1 (a
  session runs with permission prompts skipped by default) /
  [0069-image-generation-providers.md](0069-image-generation-providers.md) (the progress
  heartbeat) / [44-operator-interaction-graph.md](../log/44-operator-interaction-graph.md)

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
  nobody**.
- `BridgeApprovalGate` (the approval on shell targets) is a no-op without a conv, so the gate is
  the same as not existing. A session additionally runs with permission prompts skipped
  (ADR 0056 decision 1).

`create_session` also has two properties no other tool here has: it **recurses** (a child can
start its own children) and it **actually consumes the shared, memory-constrained host**. Absent
limits designed in from the start, it must not be opened at all.

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
  question for the ledger and the fleet overview** (ADR 0041 decision 9 / docs/44), not an
  accounting dimension, and the Meta is durable, so it can be walked later.
- `recreate` and handoff already inherit `origin_conv`; they inherit `origin_session` the same
  way ("the same slot again" does not change where it came from).

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

It does, however, **require fleet observation in the Console**. `get_session_status` (stage 1) is
the only way to watch a child, so opening spawning while observation is off would create sessions
that can be started and then never looked at again.

### 4. A session may steer only the children it started

`get_session_output` / `stop_session` / `stop_session_after_turn` / `resume_session` pass only
when the target's Meta has **`origin == "session"` and `origin_session == the caller`**. The check
lives in a handler-side gate shaped like stage 1's `memoWriteAllowed()`
(`sessionDriveAllowed(target)`). The advertised tool set is the first boundary; this is the
second.

Both conditions are required so that **a handoff successor is not a child**: a handoff is
something a person performs in the Console, and there is no reason a parent should steer an
`origin=handoff` session.

### 5. Depth is one generation — no grandchildren

**A session whose `origin_session` is non-empty may not call `create_session`.** It takes a
single lookup of the caller's own Meta, walks no chain, and therefore does not break when a
session in the middle has been deleted.

The condition is "`origin_session` is non-empty" rather than "`origin == session`" because
handing a child off produces `origin=handoff`, which would slip through. This is **deliberately a
different predicate from decision 4**: steering is narrow, recursion suppression is wide.

### 6. At most three live children per caller

Count the **not-stopped** sessions whose `origin_session` is the caller and refuse at three.
Stopped ones do not count because a running session is what actually eats the host. Together with
decision 5, the fleet is bounded at "sessions a human opened × 3".

### 7. `worktree` defaults to true, and `worktree=false` is refused on another live session's directory

The operator surface defaults to `false` (a person can say "work right here"), but the session
surface defaults to `true`. Called with defaults otherwise, parent and child **share one working
copy between two agents** — precisely the accident the workspace policy forbids by name to every
session. An explicit `worktree=false` is also refused when another live session is working in the
target directory.

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

### 10. A session-issued `stop_session` does not send `disarm_report`

The operator's `stop_session` sends `disarm_report:true`, which means "the operator withdrew its
own instruction". **A parent folding up a child does not withdraw the operator's instruction.**
Omitted, the field defaults to false, so the session surface simply does not send it. This is
decision 4's "do not touch the arm", defended at a second entrance.

### 11. Slow calls emit a progress heartbeat

`create_session` costs at worst 40 s (the POST) plus 45 s (waiting on the idempotency lookup), and
`resume_session` includes a restart wait; both exceed **opencode's 60 s per-call ceiling**. Reuse
`startProgressHeartbeat` (a `notifications/progress` every 10 s), added for image generation by
ADR 0069. claude does not count progress towards its timeout, and codex is covered by the
`tool_timeout_sec=600` the materializer stamps onto the af builtin.

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

## Rejected alternatives

- **Let `report_to` carry a session name.** ADR 0041 already rejected this. A report is addressed
  to a conversation; a session-addressed channel gives the arm two owners.
- **Reuse `origin_conv` for the parent session name.** It saves a field and puts a lie into a
  frozen dimension: `by=origin_conv` would then mix conversation names with session names.
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

- **ADR 0029's enum needs the amendment** (`session` under `origin`). The Console's
  `usage/colors.ts` holds the frozen order as a fixed table, so it is added there too. The
  amendment lands in the same commit as the code.
- **The docs/44 overview can draw lineage by reading `origin_session`.** Adding `kind:"spawn"` to
  ADR 0041 decision 9's `DispatchEntry` is not taken up: the Meta is durable and easier to walk
  after the fact than a jsonl entry.
- As in stage 1, **firing has not been confirmed on a real session**. The descriptions are written
  to specify *when* to call, so it is worth confirming a real session calls them.
- Cleaning up children that outlive their parent stays with the user (decision 13). Three live
  children do not block the parent forever: **when the parent goes, so does the limit** — it is a
  count over live children per caller, not a reservation.
