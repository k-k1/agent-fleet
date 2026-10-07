# 0111. Collaboration between tenant members: a request inbox in the CP, an opt-in limit board fed by the reaper, and nothing that lets one member's text drive another member's agent

English | [日本語](0111-member-collaboration.ja.md)

- Status: **proposed** (2026-10-07). Nothing is built. A design review by a second model (one round)
  is folded in.
- Tracking: #1840
- Related: [0057](0057-member-handoff.md) (handover between members: execution never crosses) /
  [0041](0041-cross-session-messaging.md) (peer messaging, permission laundering, live governance) /
  [0020](0020-chat-bridge.md) (Slack / Discord bridge) / [0108](0108-af-owned-agent-memory.md)
  (agent memory is evidence, not instruction) / [0061](0061-work-item-inbox.md) (work items) /
  [59-session-sharing.md](../log/59-session-sharing.md) (share ACL, RW proposals) /
  [21-memo-queue.md](../log/21-memo-queue.md) / [68-session-changed-files.md](../log/68-session-changed-files.md) /
  [69-transcript-marks.md](../log/69-transcript-marks.md) / [notification-center.md](../log/notification-center.md)

## Context

Members of one tenant can already do three things together. They can share a session read-only or
read-write (docs/59). With RW they can propose a prompt that the owner approves before it reaches the
agent. They can hand a session over, and the recipient starts the work in their own workspace (ADR
0057, P0 built, never run end to end with two accounts). Everything else about working together
happens outside Agent Fleet:

- Nobody can ask a teammate to do something and see that it was picked up, by whom, and what came of
  it (which session, which PR).
- Nobody can send a teammate a prompt ready to run unless a session is already shared with them.
- A member cannot see that a teammate is stopped on a usage limit, or when it lifts.
- Two members' agents can edit the same files for an afternoon, and nobody notices until the merge.

What exists, and where it falls short, was checked against the code:

- **The memo queue is the recipient's own.** `POST /api/memos` inserts for the caller's membership,
  and the memo bridge token is pinned to that membership (`control-plane/memo.go`,
  `control-plane/memo_bridge.go`). No path inserts into another member's queue, and a memo has no
  sender, recipe or provenance fields. Memo bodies are stored in plain text.
- **The agent reads the memo queue.** `list_memos` hands every queued memo to the session's agent
  (`workspace/agent/internal/mcpx/mcp_stdio.go`). Flushing concatenates bodies, so their origin is lost.
- **The CP already pulls from every running workspace once a minute.** The idle reaper calls
  `GET /sessions` on each running Agent (`agentSessionsEnv`, `control-plane/agent_client.go`). That
  envelope carries the live states `limited` (with `rateLimitResumeAt`) and `spend_limit`. The
  notification outbox, by contrast, is drained only when a Console asks or just before a stop
  (`control-plane/notification.go`, `drainAgentOutbox`). Switching the reaper off
  (`AF_IDLE_SWEEP_INTERVAL=0`) stops that pull too.
- **Limit windows reach the CP only from a browser.** `console/src/app/usageResetNotify.ts` posts
  observations while a Console tab is open. `notification_usage_state` keeps `resets_at` and
  `armed`. It keeps no percentage and no observation time.
- **Each kind reports limits differently.** claude reports the last statusline capture and marks a
  window `stale` once it has run past its reset. codex's `adjustWindow` rolls a past window to 0% and
  the next reset, with no stale flag. muse reports windows with their length. agy reports per-model-group
  weekly quotas and launches its TUI to read them. copilot reports credit pools. The other kinds
  report nothing beyond a session's `limited` / `spend_limit` state.
- **The recipient search is not a roster.** `GET /api/session-share-recipients` filters the tenant's
  members and returns at most 20.
- **`GET /api/events` is a per-connection snapshot differ**, not a delivery log. It has no sequence,
  replay cursor or per-recipient read state, and it sits behind `withResolved`, which resolves a
  workspace (`control-plane/events.go`).
- **Tenant-key sealing is not secrecy from the operator.** RW proposal bodies are wrapped by the
  tenant key custodian. Without a custodian they are stored base64-encoded
  (`control-plane/session_share.go`, `sealProposal`). Either way the CP can read them.
- **Marks are annotations owned by the session's workspace.** Only RW may add or remove them, and
  they are unreachable while the owner's workspace is stopped (docs/69).
- **The notification center already carries actionable session events** (answer-ready, question,
  plan approval, permission request, usage reset, handover), kept for seven days per membership.

## Decisions

### 1. Nothing a member sends can make another member's agent act

No feature here lets text from one member reach another member's agent or workspace without that
member's explicit action in between. This is ADR 0057 decision 1 applied everywhere: what crosses is
prose, git coordinates and read access. ADR 0041 named the risk, permission laundering. Concretely,
nothing a member receives may:

- stand in for the recipient's approval of a permission, a plan or a question;
- carry out work another member was denied;
- change what governs the recipient's sessions (permission settings, instruction files, MCP config,
  hooks, credentials);
- be visible to the recipient's agents before the recipient takes it in (decision 3).

The existing RW proposal keeps its owner-approval step. Pair mode, which would auto-approve RW
proposals for a while, is **not adopted** (decision 13).

### 2. The core object is a request, not a chat message

The thing only Agent Fleet can do is connect an ask to the work it caused. The CP gets one
**inbox item** object: sender, recipients (a fixed set of memberships, frozen at send time), body,
created-at, typed attachments, and optionally **request semantics**:

- states `open → claimed → done`, or `declined` / `withdrawn`;
- **one assignee**, taken by a conditional update (`open → claimed` where state is still `open`), so
  two recipients never both start it;
- links to what came of it: the session started from it, its branch, the PR. The links are written by
  the assignee's Console when it launches. Agents do not report them.

A plain message (DM) is an inbox item without request semantics. A recipe (decision 4) is an
attachment. Unread is not the same as unhandled. A request stays in the sender's and recipients'
"open" list until it reaches a final state, whether or not anyone has read it.

### 3. The inbox is its own store. The recipient takes items into the memo queue explicitly

Inbox items live in their own CP tables, keyed by recipient, with a per-recipient sequence and read
cursor. The memo API is not widened to take a destination.

- **Before it is taken in**, an item is invisible to every agent. `list_memos` does not return it, and
  no flush includes it.
- **Taking a recipe in** copies it into the recipient's memo queue as the recipient's own memo,
  carrying `origin = {sender, item id}` set by the server. The Console badges it. A flush sends it on
  its own, with its origin kept, instead of concatenating it with the recipient's own memos.
- From there it uses the memo queue's existing paths (edit, send to a session, launch). The session
  that results carries a provenance badge.

### 4. Recipes carry a repo identity, never a fetch

A recipe holds the prompt, a suggested kind / model / effort, and optionally a repository.

- The repository is a canonical identity (`host/owner/repo` for the git providers already
  connected). If a URL is kept at all, userinfo, query and fragment are stripped. A remote that does
  not normalise is refused, not passed through.
- It is shown apart from the prompt text, so the recipient sees what it points at.
- Receiving a recipe fetches and clones nothing. The recipient maps it to a working copy of their own,
  with their own connection. Cloning stays a separate, explicit action.
- A recipe is not an RW proposal. It targets the recipient's future work and needs no share.

### 5. Attaching a session grants nothing a person did not grant

- **Only the session's owner can create a share by attaching it.** The owner confirms the session,
  every recipient and the scope (the whole conversation, including what is added later). The grant is
  an ordinary `session`-scope share rule, created in the same operation as the message. If either
  fails, neither is kept.
- **A non-owner can attach a session already shared with them.** The CP then checks each recipient's
  own ACL, and recipients without access see "not shared with you". No grant is created.
- Withdrawing a message never revokes a share that existed before it.
- If rooms come (P3), joining one never extends any share.

### 6. The limit board is opt-in, reduced on the server, and honest about age

**Source.** The Agent adds a limit snapshot to the `GET /sessions` envelope the reaper already pulls
every minute. This adds no request, no token, no wake-up, and the reaper's own clocks ignore it. The
browser post stays as the fallback when the reaper is off.

**Each observation is stored with** `kind`, the quota unit (window, or model group, or credit pool),
`percent`, `resetsAt`, `observedAt` (when the Agent captured it, not when the CP received it),
`receivedAt`, source, and `stale`. An older observation never overwrites a newer one.

**Two axes, kept apart.**
- Account quota per kind and unit.
- What a session ran into: `limited` with a resume time, `spend_limit` (never shown with a resume
  time, because waiting does not clear it), `blocked`, `auth`.

A blocked session says nothing about other models or kinds. A kind with no data is "unknown", never
"available".

**Past a reset**, a window reads "reset not yet observed", not 0% and not "available". This applies
to codex's rolled-forward 0% as well, which the Agent marks as derived.

**Capability per kind.** claude, codex and muse have windows. copilot has credit pools. agy is not
collected periodically, because reading it launches its TUI. The remaining kinds show session states
only. The table lives in `guide/ref/` once built.

**Visibility.**
- Default is **hidden**.
- The member chooses who sees it (the tenant, or named members) and how much: state only, band
  (<50 / <80 / <95 / at limit), or exact.
- The CP reduces the data before it leaves, in every path: board, handover picker, SSE, export.
- Provider account e-mail, plan and raw error text never appear. Hidden and never-observed look the
  same to others.
- No ranking or comparison chart. Admin views (docs/67, docs/83) are not widened.

**Roster.** The board gets its own paginated member listing. It does not reuse the 20-row recipient
search.

### 7. Limits are never pooled

Agent Fleet never moves credentials, never runs one member's work on another member's account, never
routes work by remaining quota, and never ranks members by headroom. A person decides to take work
and runs it under their own account. Individual subscription terms restrict account sharing and
limit circumvention. Which contracts a tenant's members hold is the tenant's question (open questions).

The handover picker may show a recipient's published limit state as a hint. It does not widen the
handover ACL (still "already shared with"). It does not judge whether the recipient can run the work:
repo access, branch, kind and sign-in are checked on the recipient's side before launch, as ADR 0057
already does. The hint waits for ADR 0057's two-account end-to-end run.

### 8. Delivery: the database is the truth, SSE is a nudge

- Items, per-recipient sequence and read cursor are in the DB. `GET /api/events` gains a stream that
  says only "inbox changed, up to seq N". The Console then fetches through a membership-only REST
  route that works without a workspace record or a running workspace.
- Removing a membership, or a recipient losing access, takes effect on open connections at their next
  tick.
- The inbox has its own badge. Inbox items do not go into the notification center. That center is for
  events about one's own sessions. A conversation's read state and a backlog of open requests would
  drown it, and it keeps seven days, while open requests must not expire silently.

### 9. Confidentiality and lifecycle

- **What the seal promises.** Bodies are sealed with the tenant key custodian when one exists.
  Sealing protects against a database leak, not against the operator: the CP can decrypt. The
  product does not promise "secret from admins". Where no custodian exists, Settings states that
  bodies are stored unencrypted.
- **No admin read API in P1.** Whether admins may read or export bodies is an open question. If it
  comes, every read is audited and visible to the participants.
- **Separate lifecycles.**

| Thing | Ends when |
|---|---|
| Inbox item body | retention expiry, or the last participant's membership is removed |
| Request state and links | kept with the item |
| A recipe taken into the memo queue | it is the recipient's memo from then on and follows memo retention; it is never pulled back |
| A share grant made with a message | an ordinary share, revoked like any other |
| Read cursors | removed with the membership |

- Expired rows are filtered before decryption, never after.
- The audit log records who sent which kind of item to whom, and when. It never records a body.

### 10. Receiving is under the recipient's control

The recipient controls what they receive:
- accept from the whole tenant or from chosen members only;
- block a sender;
- do-not-disturb, which mutes badges and toasts but not delivery, and never counts as a decline.

The CP enforces limits:
- body size;
- pending items per sender → recipient pair;
- sends per sender per hour;
- recipients per item.

Admin announcements (P2) are a separate kind with their own rule. They cannot be blocked, and only
admins can send them.

### 11. Agents may draft, people send

An agent may write a draft message or recipe into its own user's composer, as `add_memo` already
writes to the user's memo queue. The user picks recipients and presses send. No agent gets a send
API, the roster or another member's inbox.

### 12. Edit-overlap candidates are a separate consent, and never a safety claim

The CP may compare paths that different members' sessions edited in the same repository, and say
"B's session also edited `x.go`". This moves file paths out of the workspace, so it gets its own rule:

- Separate opt-in per member and repository. A repo-scope share does not imply it, because the share
  DTO deliberately drops file coordinates.
- Only repo-relative paths. Paths outside the working copy, absolute paths and secret-looking names
  (`.env*`, `*.pem`, …) are dropped.
- Repository identity across members needs its own design. `workingCopyId` is per owner and per
  working copy, not tenant-wide.
- The data comes from transcript edit history (docs/68). It misses shell and formatter edits, keeps
  old edits, and differs by kind. The feature is named "recent edit overlap candidates", shows the
  observation time and the kinds covered, and **never says "no overlap"**.

### 13. Pair mode, distributable skills, team memory and the chat bridge go to separate ADRs

- **Pair mode** would be continuous delegation of a member's agent, and that member's billing, to
  someone else. RW proposals include answers to questions and plans. Done safely, it needs a
  server-side, revocable, capped delegation that is checked at every claim. Not adopted. Not
  recommended now.
- **Skills** can carry standing instructions, scripts and hooks. Installing one is a governance
  change, unlike copying a prompt. A versioned, human-facing **recipe shelf** can come first (P3).
  Skill distribution needs its own install / update review.
- **Team knowledge memory.** Publishing must be approved by a person authenticated to the CP, never
  through an Agent token: inside the Agent, a session could approve itself. ADR 0108 keeps memory as
  evidence, not instruction. That has to hold across members too.
- **Chat bridge mirroring.** The bridge lives in each member's own workspace, with their own token. It
  cannot deliver CP-held items while that workspace is stopped, and it accepts input only from the
  bound person. Mirroring needs sender consent and recipient opt-in. Room replies must never become
  agent input. Moving tokens to the CP would need its own ADR. Messages stay Console-only.

### 14. Phase order

| Phase | Contents | Why here |
|---|---|---|
| **P0** | Accept this ADR. Run ADR 0057's two-account handover end to end. | Everything below assumes two members on one fleet actually work. |
| **P1, lane A** | Inbox store with sequence and read cursor (decisions 2, 3, 8–10). DMs. Requests with claim / decline / done and the session / PR links. Recipes and explicit take-in. Receive controls. Agent drafts (decision 11). | The request is the product's own value. It needs no new Agent capability and no workspace. |
| **P1, lane B** | Limit board (decision 6): reaper snapshot, per-kind capability, hidden by default, server-side reduction. | Independent of lane A. Pulled through an existing call. |
| **P2** | Limit hint in the handover picker (after P0). Session follow (state and link only, opt-in, never permission contents or answer buttons). RO review with a new annotate-only permission and review comments held in the CP. `session` / `workItem` / `pr` attachments. Tenant announcements. | Each needs the P1 store. Follow also needs a CP-side event path for stopped workspaces. |
| **P3** | Small rooms. Versioned recipe shelf. An opt-in list of completion reports, then a digest. Recent edit overlap candidates. | Wait for real use of P1/P2. Overlap needs repository identity and its own consent. |
| **Separate ADRs** | Pair mode (not recommended). Skill distribution. Team knowledge memory. Chat bridge mirroring. | Each widens what another member's text can do to an agent. |

## Rejected options

- **A general chat product first.** Slack already does general chat, and the bridge reaches it. Rooms
  wait until requests are in use (P3).
- **Peer messaging across workspaces.** `send_to_peer_session` addresses a session, not a person,
  costs the receiving agent a turn, and is the channel ADR 0041 had to fence. People talking to people
  should not go through an agent.
- **Inserting into another member's memo queue.** The agent reads the queue. Text from a colleague
  would read as the user's own instruction.
- **A browser-fed limit board.** It updates only while the member has a tab open, which is exactly when
  others least need to know.
- **State visible by default.** "State only" still exposes working hours and billing trouble. Opt-in
  means hidden until chosen.
- **Treating a past reset as 0%.** A capture that stopped would read as fresh headroom.
- **Recipes injected straight into a running session.** That is an RW proposal without the share.
- **Quota pooling, routing by headroom, a shared service account.** See decision 7.
- **Pair mode as a time-boxed UI switch.** A browser timer cannot hold expiry, revocation or replica
  boundaries. See decision 13.

## Open questions

- Retention defaults (proposal: inbox items 90 days, open requests until a final state, then 90 days).
- Admin read or export of bodies: none, or audited and visible to the participants.
- Quota unit on the board: membership × kind, or provider account / model group. What happens on
  re-login under a different account, and for a member of several tenants?
- Which subscription contracts the tenant's members hold, and whether its admin wants the limit board
  at all (a tenant-level switch).
- Expected tenant size (a handful, or dozens), which sizes the roster, rate limits and rooms.
- Acceptance for P1: version skew between the CP and Agents, a recipient with no workspace yet, a
  revoked share, a removed member, two simultaneous claims.
