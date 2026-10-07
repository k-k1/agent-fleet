# 0111. Collaboration between tenant members: a request inbox in the CP, an opt-in limit board fed by the reaper, and nothing that lets one member's text drive another member's agent

English | [日本語](0111-member-collaboration.ja.md)

- Status: **proposed** (2026-10-07). Nothing is built. Two rounds of design review by a second model
  are folded in.
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
0057; P0 is built, and no two-account end-to-end run on a real fleet is recorded in the docs).
What is missing:

- Nobody can ask a teammate to do something and see that it was picked up, by whom, and what came of
  it (which session, which PR).
- Nobody can send a teammate a prompt ready to run unless a session is already shared with them.
- A shared session's row already shows `limited` / `spend_limit`. But nothing shows a member's account
  quota, or anything about sessions they have not shared, and a member cannot choose to publish it.
- Two members' agents can edit the same files for an afternoon, and nobody notices until the merge.

What exists, and where it falls short, was checked against the code:

- **The memo queue is the recipient's own.** `POST /api/memos` inserts for the caller's membership,
  and the memo bridge token is pinned to that membership (`control-plane/memo.go`,
  `control-plane/memo_bridge.go`). No path inserts into another member's queue, and a memo has no
  sender, recipe or provenance fields. Memo bodies are stored in plain text.
- **The agent reads the memo queue.** `list_memos` hands every queued memo to the session's agent
  (`workspace/agent/internal/mcpx/mcp_stdio.go`). Flushing concatenates bodies, so their origin is lost.
- **The CP already pulls from running workspaces.** The idle reaper sweeps serially, by default once a
  minute (`AF_IDLE_SWEEP_INTERVAL`), and calls `GET /sessions` on each running Agent
  (`agentSessionsEnv`, `control-plane/agent_client.go`). It skips a tenant whose idle tiers are all off
  (`tierClocks.anyOn`, `control-plane/reaper.go`), and it does not run at all when the interval is 0.
  The envelope carries the live states `limited` (with an optional `rateLimitResumeAt`) and
  `spend_limit`. The notification outbox is drained only when a Console asks or just before a stop
  (`control-plane/notification.go`, `drainAgentOutbox`). The reaper's `GET /sessions` does not drain it.
- **Limit windows reach the CP only from a browser.** `console/src/app/usageResetNotify.ts` posts
  `windowKey / percent / resetsAt` for claude and codex, 5h and 7d only, while a Console tab is open.
  `notification_usage_state` keeps `resets_at` and `armed`. It keeps no percentage and no
  observation time.
- **Each kind reports limits differently, and not always honestly about age.**
  - claude: reports the last statusline capture, and marks a window `stale` once it has run past its
    reset.
  - codex: `adjustWindow` rolls a past window to 0% and the next reset with no stale flag. Its account
    cache restamps `fetched` even when the fetch fails, so a failed refresh makes an old reading look
    seconds old (`codex/usage.go`, `accountUsage`).
  - muse: reports windows with their length.
  - agy: reports per-model-group weekly quotas, plus five-hour quotas on paid tiers. A refresh scrapes
    its TUI, with a five-minute cache.
  - copilot: reports credit pools.
  - Detecting that a session hit a limit (the rate-limit watch) covers claude, muse and Managed codex.
    The other kinds and execution methods have no such contract.
- **The recipient search is not a roster.** `GET /api/session-share-recipients` filters the tenant's
  members and returns at most 20.
- **`GET /api/events` is a per-connection snapshot differ**, not a delivery log. It has no sequence,
  replay cursor or per-recipient read state. It sits behind `withResolved`, which creates a workspace
  record for a member opening the Console for the first time (`control-plane/events.go`,
  `control-plane/resolver.go`).
- **Tenant-key sealing is not secrecy from the operator.** RW proposal bodies are wrapped by the
  tenant key custodian. Without a custodian they are stored base64-encoded
  (`control-plane/session_share.go`, `sealProposal`). Either way the CP can read them.
- **Marks are annotations owned by the session's workspace.** Only RW may add or remove them, and
  they are unreachable while the owner's workspace is stopped (docs/69).
- **The notification center is a membership's event history, kept seven days.** It holds answer-ready,
  question, plan approval, permission request, usage reset and handover events. It has no notion of
  a conversation's read state, or of a request that is still open.
- **The handover recipient's working copy is guessed.** The accept row matches the remote's basename
  against the recipient's working copies and falls back to the first one. It does not verify repo
  identity or HEAD (`console/src/features/sharing/HandoffOfferRow.tsx`).

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
created-at, typed attachments, and optionally **request semantics**. A plain message (DM) is an
inbox item without them. A recipe (decision 4) is an attachment.

**State lives in two places.** The item has a shared state: `open`, `claimed` (with one assignee),
`done`, `declined`, `withdrawn`. Each recipient has their own delivery state: unread, read, declined.

| Who | Operation | Allowed from | Effect |
|---|---|---|---|
| a recipient | claim | `open` | `claimed`, assignee = caller, by a conditional update on the state still being `open` |
| a recipient | decline | `open` | that recipient's delivery is `declined`. When every recipient has declined, the item becomes `declined` |
| the assignee | release | `claimed` | back to `open` |
| the assignee | done | `claimed` | `done` |
| the sender | withdraw | `open`, `claimed` | `withdrawn`. The assignee is told |
| the system | assignee's membership removed, or assignee blocks the sender | `claimed` | back to `open` |
| the system | sender's membership removed | `open`, `claimed` | `withdrawn` |

**What the claim guarantees, and what it does not.** It guarantees one assignee on record, and that
the product's own start actions for a request do not race. A recipe attached to a request can be
taken in or launched only by the current assignee, which the CP checks. Take-in is idempotent per
(item, assignee), so a double click or a retry yields one memo. The claim cannot stop someone who
copies the text by hand. Withdraw stops further adoption of the request and tells the assignee. It
never stops work already running and never recalls a copy (decision 9). A recipe attached to a
plain DM has no claim, and any recipient may take it in.

**Links to what came of it.**
- On a successful launch from a request, the assignee's Console records the session and the working
  copy and branch at that moment.
- The assignee adds or updates the PR and the final branch by hand from the request card. At launch
  time there is no PR yet, and the branch can change.
- Agents never report links. Finding the PR automatically through the git connections is a separate,
  later step. It has to handle branch-name collisions and lost access.

Unread is not the same as unhandled. A request stays in the sender's and the assignee's "open" list
until it reaches a final state, whether or not anyone has read it.

### 3. The inbox is its own store. The recipient takes items into the memo queue explicitly

Inbox items live in their own CP tables, keyed by recipient, with a per-recipient sequence and read
cursor. The memo API is not widened to take a destination.

- **Before it is taken in**, an item is invisible to every agent. `list_memos` does not return it, and
  no flush includes it.
- **Taking a recipe in** copies it into the recipient's memo queue as the recipient's own memo, with
  `origin = {sender, item id}` set by the server. Editing the memo never removes its origin. The
  Console badges it. A flush sends it on its own, with its origin kept, instead of concatenating it
  with the recipient's own memos.
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

### 5. Attaching a session grants nothing a person did not grant, and shows nothing to those without access

- **Only the session's owner can create a share by attaching it.** The owner confirms the session,
  every recipient and the scope (the whole conversation, including what is added later). The grant is
  an ordinary `session`-scope share rule, created in the same operation as the message. If either
  fails, neither is kept.
- **A non-owner can attach a session already shared with them.** No grant is created.
- **What a recipient sees is decided per recipient, on every read.**
  - A recipient with access sees the attachment.
  - A recipient without access sees an opaque placeholder made by the server. It carries no catalog
    id, title, repo coordinate, branch, owner or preview.
  - The ACL is evaluated again on every list, detail, link resolution and export. A share revoked
    after sending turns the card into the placeholder.
  - The sender's own prose in the message is shown as written. Metadata the CP fetched from a
    resource is not.
- Withdrawing a message never revokes a share that existed before it.
- If rooms come (P3), joining one never extends any share.

### 6. The limit board is opt-in, reduced on the server, and honest about age

**Source: a snapshot the Agent has already captured.**
- The Agent adds a limit snapshot to the `GET /sessions` envelope the reaper already pulls. Building
  the envelope only reads what collectors have already captured. Provider refreshes run on their own
  bounded schedule, and the envelope never waits on them.
- The snapshot is optional and isolated. A malformed or unknown-version snapshot makes only that
  kind's quota `unknown`. It never fails decoding of the sessions, repo jobs or image jobs, and never
  changes an idle, busy or presence decision.
- **Coverage.** The reaper reaches only running workspaces in tenants with an idle tier on, at its
  configured interval, serially. Where it does not reach, the browser post is the source. The board
  promises no refresh rate.

**Observation contract, shared by both sources.**
- `observedAt` is the time of the last successful real observation by the provider. A failed refresh
  and a cache read never move it. Collectors that restamp on failure (codex's account cache today)
  are fixed as part of this work.
- `receivedAt` and the source are kept separately.
- A derived value (a past window rolled to 0%) keeps the original `observedAt` and is flagged
  `derived`. It never clears "reset not yet observed".
- A private **login epoch** per kind changes when the member signs in to a different provider
  account. Observations from an older epoch are dropped, never shown as the new account's. The epoch
  is never shown to others.
- An older observation never overwrites a newer one. Identical observations from the reaper and the
  browser are deduplicated on (membership, kind, unit, epoch, `observedAt`).
- Past an age threshold an observation is `stale`. A kind whose observation time is unknown is `unknown`.
- The browser post (`POST /api/notifications/usage-observations`) is revised to carry the same
  fields. It does not substitute the time it sends for `observedAt`. The usage-reset notification's
  `armed` state stays separate, and is never set by a derived or stale value.

**Two axes, kept apart.**
- Account quota per kind and unit (window, model group, credit pool).
- What a session ran into: `limited`, with an automatic-resume time when one was scheduled (it is
  optional), `spend_limit` (never shown with a resume time, because waiting does not clear it),
  `blocked`, `auth`.

A blocked session says nothing about other models or kinds. A kind with no data is "unknown", never
"available".

**Past a reset**, a window reads "reset not yet observed", not 0% and not "available".

**Coverage in P1.**

| Kind | Quota | Session limit state |
|---|---|---|
| claude | 5h / 7d windows | yes |
| codex | 5h / 7d windows | Managed only |
| muse | windows with their length | yes |
| copilot | credit pools | unknown |
| agy | not collected by the board: a refresh scrapes the TUI. A cached reading with its time may be carried later | unknown |
| others | unknown | unknown |

Anything not in this table is `unknown`. The full table lives in `guide/ref/` once built.

**Visibility.**
- Default is **hidden**.
- The member chooses who sees it (the tenant, or named members) and how much: state only, band
  (<50 / <80 / <95 / ≥95 "near limit"), or exact. Bands are per unit and never aggregated. A real
  limit hit is the separate session state, never the top band.
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

The handover picker may show a recipient's published limit state as a hint.
- It does not widen the handover ACL (still "already shared with").
- It does not judge whether the recipient can run the work. Today's handover accept row guesses the
  working copy by remote basename and verifies neither repo identity nor HEAD. Checking repo identity,
  branch / commit, kind and execution method, and sign-in before launch is a requirement for the
  handover follow-up, not something this hint provides.

### 8. Delivery: the database is the truth, SSE is a nudge, and the inbox never needs a workspace

- Items, per-recipient sequence and read cursor are in the DB. The Console reads and writes the inbox
  through membership-only REST routes (`withMembership`).
- The live nudge is **a membership-only inbox stream of its own**, not a stream added to
  `GET /api/events`. That route goes through `withResolved`, and would create a workspace record for a
  member who only uses the inbox. The new stream keeps what `withResolved` gives the existing one:
  the member-connection registration, and cancellation when the membership is removed. Where the
  stream is unavailable, the Console polls the REST route.
- Acceptance: ordinary use of the inbox never creates a workspace record or starts a workspace.
- The inbox has its own badge, and items do not go into the notification center. That center is a
  seven-day history of events. An open request must neither expire silently nor sit among answer-ready
  events, and reading a conversation is a different kind of "read".

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
| Inbox item body | retention expiry after a final state, or the last participant's membership is removed |
| Request state and links | kept with the item |
| A recipe taken into the memo queue | it is the recipient's memo from then on and follows memo retention; it is never pulled back |
| A share grant made with a message | an ordinary share, revoked like any other |
| Read cursors | removed with the membership |
| Audit records (no body) | their own retention, set apart from bodies |

- **Purging is the CP's job.** An idempotent CP-side purge deletes expired and orphaned bodies,
  whether or not anyone reads them and whether or not any workspace or browser is running. Reads also
  filter expired rows before decryption. Deletion from the live database is stated separately from
  how long backups keep a copy.
- **Open requests do not expire silently.** After a threshold they are marked as long-open in the
  sender's and assignee's lists, and either side can close them: the sender withdraws, the assignee
  releases or marks done. They count against the pending limit (decision 10) until then.
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

In P1, an agent drafts through the tool it already has. It writes its user's own memo with
`add_memo`, and the user turns that memo into a message or recipe in the Console, then picks
recipients and presses send. No new agent tool is needed for this. A dedicated draft tool can come
later, as an optional step that does not change the inbox's no-workspace property. No agent gets a
send API, the roster or another member's inbox.

### 12. Edit-overlap candidates need two consents and an audience, and never make a safety claim

The CP may compare paths that different members' sessions edited in the same repository, and say
"B's session also edited `x.go`". This moves file paths out of the workspace, so it gets its own rules.

- **Two consents per member and repository:**
  1. exporting the paths at all;
  2. who may see them: the tenant, or named members.

  A repo-scope share does not imply either one, because the share DTO deliberately drops file
  coordinates. A limit-board audience does not imply either one.
- **Projection.** The CP shows a candidate to a viewer only when both sides' audiences include that
  viewer. A session's name, title or link in a candidate is further subject to that session's share
  ACL. Exposing a path never opens the conversation.
- **What crosses.** Only repo-relative paths. Paths outside the working copy, absolute paths and
  secret-looking names (`.env*`, `*.pem`, …) are dropped.
- **Withdrawal.** Withdrawing consent, or leaving the tenant, invalidates the CP's cached paths and
  any candidates computed from them.
- **Repository identity** across members needs its own design. `workingCopyId` is per owner and per
  working copy, not tenant-wide.
- **Honesty.** The data comes from transcript edit history (docs/68). It misses shell and formatter
  edits, keeps old edits, and differs by kind. The feature is named "recent edit overlap candidates",
  shows the observation time and the kinds covered, and **never says "no overlap"**.

If the audience model is not settled, the feature waits for its own ADR.

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

### 14. Phase order, with dependencies per feature

| Phase | Contents |
|---|---|
| **P0** | Accept this ADR. Write the two-membership acceptance conditions for the inbox: auth, recipients, claim race, removal, a member without a workspace. Run ADR 0057's two-account handover end to end, in parallel. |
| **P1-A, first half** | The inbox: durable store, membership-only REST and stream, receive controls, DMs, requests with the state table, session links recorded at launch, manual PR links. Agent drafts through `add_memo`. |
| **P1-A, second half** | Recipes: explicit take-in with origin, assignee-only and idempotent take-in for requests. Session attachments that create a share, once decision 5's transaction is verified alone. |
| **P1-B** (independent of A) | The limit board: the lightweight snapshot, the observation contract, the login epoch, hidden by default, server-side reduction, P1 coverage only. |
| **P2** | Limit hint in the handover picker. Session follow, starting as the last observed state with its time. RO review. `workItem` / `pr` attachments. Tenant announcements, if asked for. |
| **P3** | An opt-in list of closed requests and completion reports, then a digest if it gets used. A versioned recipe shelf. Small rooms. Edit-overlap candidates, once audience and repository identity are settled (or a separate ADR). |
| **Separate ADRs** | Pair mode (not recommended). Skill distribution. Team knowledge memory. Chat bridge mirroring. |

Dependencies, per feature:

| Feature | Needs |
|---|---|
| Inbox (P1-A) | P0's acceptance conditions only. It does not wait for the handover run. |
| Handover hint (P2) | P1-B and the two-account handover run. Not the inbox. |
| Session follow (P2) | As state only: P1-B's snapshot path. As transition notifications: a durable event path from running workspaces with no browser open (outbox drain with ack and event ids, drain before stop). That is its own piece of work. Permission and question contents and answer buttons never reach other members. |
| RO review (P2) | A new annotate-only permission, and review comments stored in the CP rather than as marks in the owner's workspace. |
| Session attachments with a new share | Decision 5's transaction. |

## Rejected options

- **A general chat product first.** Slack already does general chat, and the bridge reaches it. Rooms
  wait until requests are in use (P3).
- **Peer messaging across workspaces.** `send_to_peer_session` addresses a session, not a person,
  costs the receiving agent a turn, and is the channel ADR 0041 had to fence. People talking to people
  should not go through an agent.
- **Inserting into another member's memo queue.** The agent reads the queue. Text from a colleague
  would read as the user's own instruction.
- **A browser-fed limit board only.** It updates only while the member has a tab open, which is exactly
  when others least need to know.
- **A dedicated Agent→CP push endpoint and token for limits.** The reaper's existing authenticated
  pull already reaches running workspaces. A new inbound path would add a credential for no coverage
  the browser fallback does not already give.
- **State visible by default.** "State only" still exposes working hours and billing trouble. Opt-in
  means hidden until chosen.
- **Treating a past reset as 0%.** A capture that stopped would read as fresh headroom.
- **Recipes injected straight into a running session.** That is an RW proposal without the share.
- **Adding the inbox to `GET /api/events`.** It would make the inbox create workspace records.
- **Quota pooling, routing by headroom, a shared service account.** See decision 7.
- **Pair mode as a time-boxed UI switch.** A browser timer cannot hold expiry, revocation or replica
  boundaries. See decision 13.

## Open questions

- Retention defaults (proposal: items 90 days after a final state; long-open marking after 14 days).
- Admin read or export of bodies: none, or audited and visible to the participants.
- For a member of several tenants, whether one publication setting covers all of them.
- Which subscription contracts the tenant's members hold, and whether its admin wants the limit board
  at all (a tenant-level switch).
- Expected tenant size (a handful, or dozens), which sizes the roster, rate limits and rooms.
- The stale threshold for the board, and the long-open threshold for requests.
