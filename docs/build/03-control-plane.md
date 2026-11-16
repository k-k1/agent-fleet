---
audience: "someone changing the Control Plane"
source_of_truth: "the code (this is a map and a statement of intent)"
---

# 03. Control Plane

English | [日本語](03-control-plane.ja.md)

The CP is the only resident backend outside the workspaces — one Go binary. The
browser only ever talks to it, and **it never touches tmux or a working copy itself**:
everything in a workspace goes through the agent ([01 §1.3](01-architecture.md)). The
bare repositories of the internal git provider are the CP's own. This chapter is "what
lives in the CP and how it connects". The wire contracts are [05](05-api.md); the
security design is [07](07-security.md).

## 3.1 Responsibilities

- **Serving the Console** — the built bundle from `CONSOLE_DIR` on the `/` catch-all.
  The entry points (`index.html`, `version.json`, `sw.js`, the manifest) are `no-store`,
  so a deployment is live at the next load; the content-hashed files under `/assets/`
  are `public, max-age=31536000, immutable` (`registerStatic`). The favicon, PWA icons
  and manifest are recoloured on the fly when the deployment is branded (`brand.go`).
- **The auth gate (L1)** — with `AUTH=oauth` the CP *is* the edge: it verifies the
  signed cookie on every request, re-checks the allowlist so offboarding takes effect
  before the cookie expires, **deletes any inbound identity header and re-injects the
  verified value**, and fails closed. `dev` and `proxy` have no gate
  ([07 §7.3](07-security.md)).
- **Resolving identity and tenant** — verified email → identity; `X-AF-Tenant` (the
  `tenant` query parameter where a browser cannot set headers) checked against a
  membership. Provisioning an unknown identity (`AF_PROVISION`, `auto` or `invite`) and
  the deployment-administrator role (`SUPER_ADMIN_EMAILS`, which also demotes anyone no
  longer listed at boot) happen here too (§3.2).
- **Workspace lifecycle** — one membership means one workspace: allocation, start,
  stop, recreate, clean-home, and mirroring the state to the database. The substrate is
  behind the Runtime adapter (§3.3).
- **Relaying to the agent** — five paths: REST, SSE, terminal WebSocket, browser
  REST + WebSocket, and preview ([05 §5.3](05-api.md)). **The REST relay is an
  allowlist**: every relayed path is a route the CP registers (a row in a relay table,
  `relays_*.go`, or a wrapper in `routes.go`), so an agent endpoint without a CP route is
  unreachable from the browser. Preview also answers on per-workspace subdomains when
  `AF_PREVIEW_DOMAIN` is set ([decisions/0062](../decisions/0062-preview-subdomain.md)).
- **The Console's push channel** — `GET /api/events` is the CP's own SSE stream. It
  folds the workspace, sessions, stats, notifications and engines polls into one
  connection and sends only the streams whose JSON changed. It does not count as
  activity for idle-stop.
- **The workspace → CP bridges** — endpoints under `/internal/` that an agent calls
  with a per-membership token the CP injected at start (§3.4): memos, schedules, the
  tenant MCP registry, documentation, engine tokens, the git OAuth refresh and AWS
  profiles.
- **The metadata store** — SQLite by default (`AF_DB`), Postgres when `AF_DATABASE_URL`
  or `AF_DB_HOST` is set ([06](06-data.md)).
- **Audit** — selected mutating relays, the admin API, MCP writes and system jobs all
  record. Where it is written is [05 §5.5](05-api.md); the design is
  [07 §7.7](07-security.md).
- **An MCP server** — exposes member and admin tools to an external client (§3.5).
- **A built-in git provider** — bare repositories over smart HTTP with LFS, hosted by
  the CP itself, **not through the agent** ([91](91-internal-git.md)).
- **Egress control** — distributing policy to the forward proxy, aggregating observed
  events, and the admin and member APIs (§3.8).
- **The memo queue** — per-membership memos and batch send. The CRUD of memo text and
  categories is **CP-only**, so it works while the workspace is stopped; the flush and
  the image attachments need the agent (§3.6).
- **The scheduler** — schedule definitions live in the CP's database and a goroutine
  fires them, resolving time zones (including DST) from an embedded IANA database.
  Creating one goes through `/internal/schedules` (`AF_SCHEDULE_TOKEN`), which the
  operator conversation uses to turn natural language into a spec; the Console's
  `/api/schedules` can list, edit, pause, resume, run and delete, but not create
  ([decisions/0021](../decisions/0021-scheduled-execution.md)).
- **Notifications** — the agent's outbox is drained into the CP's store when fetched,
  and exposed as a list with read state (`/api/notifications`, kept 7 days).
- **The tenant MCP registry** — server definitions a tenant administrator distributes to
  every member (`/api/admin/mcp-servers`, polled by the agent at `/internal/mcp-servers`).
  A member's own registrations are composed on the agent side and merely relayed
  ([decisions/0031](../decisions/0031-mcp-registry.md)).
- **Self-hosted engines** — the engine table and model catalogue, the gateway a
  workspace reaches them through, and starting and stopping them (§3.9).
- **Speech** — the CP calls VOICEVOX or Polly itself (`/api/tts/*`); on AWS, VOICEVOX
  is an on-demand ECS service driven by the same controller as the engines
  ([decisions/0070](../decisions/0070-tts-ondemand-engine.md)).
- **Cost and usage** — occupancy seconds on every target, and the AWS invoice
  attributed per member where there is one (§3.7,
  [decisions/0048](../decisions/0048-member-cloud-cost.md)).
- **What crosses members** — the CP holds the rules and asks the owner's agent for the
  content: session sharing (the share rules are evaluated against the database on every
  request), handoff offers ([decisions/0057](../decisions/0057-member-handoff.md)), and
  the work-item inbox, where the CP keeps the saved queries and a cache of non-secret
  metadata while the agent fetches with its own provider tokens
  ([decisions/0061](../decisions/0061-work-item-inbox.md)).
- **Background jobs** — the reaper, the usage sampler, the cloud-cost poller, git GC,
  the audit sweep and the ecs-ec2 pool jobs (§3.7).

Some features are the *agent's*, and the CP only relays them: cleanup, agent memory
management ([decisions/0022](../decisions/0022-agent-memory-management.md)), the
session trash ([decisions/0101](../decisions/0101-session-delete-via-trash.md)), and
image-generation jobs and studios
([decisions/0081](../decisions/0081-image-generation-pane.md),
[0100](../decisions/0100-image-generation-studio.md)).

Which file implements what is [90-code-map](90-code-map.md).

## 3.2 The life of a request

A member's API call goes through the same front half (the authorisation principles
and error shapes are [05 §5.4](05-api.md)). The steps below are the full form,
`withResolved`, the standard wrapper for the member-facing `/api` routes. CP-only
routes — the memo CRUD, schedules, the saved work-item queries — use `withMembership`,
which stops after step 3 and never builds a Runtime; the memo flush and the memo image
routes need the agent and use `withResolved`. Routes that need no tenant at all (PATs,
the tenant picker) use `withIdentity` and stop after step 2. Preview has a wrapper of
its own (`withPreviewResolved`), because a new tab cannot carry the tenant header.

1. **The auth gate** (oauth mode only) — verify the cookie and inject the email. The
   exemptions are declared next to the routes they belong to (`exemptExact` /
   `exemptPrefix`): the login and OAuth routes, `/healthz` and `/readyz`, and the
   surfaces that authenticate themselves — `/mcp` (a bearer PAT), `/git/` (basic auth
   with a git token), `/internal/` (per-purpose bridge tokens) and `/engine/` (an engine
   session token) ([07 §7.3](07-security.md)).
2. **Resolve the identity** — email → identity (`dev` uses the fixed `DEV_USER`). A
   person with no membership joins a tenant whose auto-join domain matches, or is
   provisioned into the default tenant (`AF_PROVISION=auto`) or refused (`invite`).
3. **Check the membership** — `X-AF-Tenant`, then the query fallback.
4. **Resolve the workspace runtime** — membership → workspace row (allocating one if
   needed, §3.3) → unwrap the DEK (§3.4) → build the Runtime through the factory,
   cached in memory but with the database as the truth.
5. **Handle or proxy** — CP-only surfaces are answered here; everything else goes to the
   agent over one of the five paths ([05 §5.3](05-api.md)).

Idle-stop's activity clock is advanced where the code calls `touchWorkspace`, path by
path — writes through the relay, streams, connections, preview traffic, explicit
starts, the attention beacon and a scheduled wake among them. Reading, as a rule, does
**not** count — for example a relayed `GET` or `HEAD`, `/api/events`, and the reads and
polls the CP answers itself (`GET /api/workspace`, the memo list). These examples are
not a complete list. A Console left open therefore never keeps a workspace warm.

What a request meets when the workspace is not running depends on its path
([05 §5.3](05-api.md)). The general REST relay (`agentProxyAPI.rest`) does not check
the state; it dials, and an agent it cannot reach is a `502`. A terminal checks first and
answers `409 workspace_starting` or `409 workspace_stopped`; a login flow refuses with
`409 workspace_starting` until the workspace is `running`. **Starting a workspace does
not wait for its agent**: `POST /api/workspace/start` returns the live
state as soon as the launch is committed, which on ECS reads `starting` until the task
converges, and the Console keeps polling.

**The requests that need the agent in their very next step** — creating, forking or
resuming a session (`POST /api/sessions`, `…/fork`, `…/start`), a carried answer
(`…/carried-answer`) and the SSM node lookup — start a stopped workspace themselves
(`AF_AUTOSTART`, on by default) and wait for the agent in `ensureWorkspaceReady`: 55
seconds by default (`AF_AGENT_READY_WAIT_SEC`), measured from the request's arrival so
the start's own wait is counted. Past that they answer `409 workspace_starting` and the
boot carries on, so a retry gets through. The wait has to stay below the ingress's idle
timeout (60 seconds on the AWS load balancer), or the caller sees a 504 instead.
Session create and fork wait **before** the session quota, because the quota counts the
agent's live sessions. Attaching a terminal, the attention beacon and anything
read-only never start a workspace.

## 3.3 The manager and the Runtime abstraction

- **The manager** allocates each membership's resources once and persists them: the
  names (`af-ws-<slug>-<key>`, `af-net-<slug>-<key>` and `<WS_DATA>/<slug>/<key>`; the
  default tenant keeps the slug-free `af-ws-<key>` form **for compatibility with
  deployments that already exist** — `manager.workspaceNames`), the agent port (counted
  up from `WS_AGENT_PORT`), and `AGENT_TOKEN`. Across a CP restart the database row is
  the truth, and the state is read from the substrate rather than recreated.
- **`Runtime` / `RuntimeFactory`** abstract the substrate, and **every call site** —
  handlers, the reaper, admin, MCP — builds through the factory. `AF_RUNTIME` picks one
  of `docker` (the default, also `local`), `native` (`wsl`), `ecs` (`aws`) or `ecs-ec2`
  (`runtime.NewFactory`); anything else fails at boot. What each can do is
  [ref/deploy-targets](../../guide/ref/deploy-targets.md); choosing one is
  [09](09-deploy.md).
- **`Start` returns once the launch is committed, not once the agent answers** (the
  `Runtime` interface's contract). The local adapters wait a courtesy grace on
  `/healthz`; ECS commits a service and converges asynchronously. `State` reports
  `running`, `starting`, `stopped` or `none`, and a `starting` workspace must be neither
  started again nor idle-stopped. A readiness overrun is not an error. On `docker`,
  Start removes any stopped remnant, runs the current image with the home and the
  Claude config mounted and the tokens and keys in the environment.
- **Stop is two-stage and graceful**: SIGTERM, a grace period (`AF_STOP_GRACE_SEC`, 30
  s), then SIGKILL. The agent is handed a *shorter* grace (`AGENT_STOP_GRACE_SEC`),
  deliberately, so it can interrupt the pane and let tmux exit before the hammer falls.
- **Lifecycle operations are serialised per workspace** — start, stop, recreate and
  clean-home take a local lock and a lease in the database, so neither a concurrent
  request nor another CP replica can interleave with a check-then-start.
- **Connection tracking** counts long-lived connections, per-session attachment, the
  last recorded activity (§3.2) and the last terminal keystroke, in memory, and publishes a
  renewable presence lease to the database for other replicas. This is what the reaper
  reads (§3.7). A terminal counts as presence only while it is typed in
  (`AF_PRESENCE_IDLE_TIMEOUT`, 30 minutes); a browser pane counts only while it is
  visible; `POST /api/workspace/attention` is the Console's beacon for a person reading
  without typing.

## 3.4 Wiring at start: keys and tokens

The cryptography itself — envelope encryption, key derivation, the limits of
crypto-shredding — is [07 §7.6](07-security.md). Only the wiring is here:

- **At boot**, `AF_MASTER_KEY` is hashed into the master key and the key custodian
  (`localCustodian`) is built from it. Without it there is no encryption at all
  (development only).
- **When a workspace is resolved**, the wrapped DEK is unwrapped by the custodian (the
  first time, a legacy DEK is derived and wrapped — a compatibility point that avoids
  re-encrypting an existing store), and the plaintext DEK is injected as
  `AF_SECRET_KEY`. **The agent is indifferent to the scheme and never learns where the
  key came from.**
- **The bridge tokens** are injected at the same time (`workspaceExtraEnv`): one
  per-membership token per purpose — `AF_INTERNAL_GIT_TOKEN`, `AF_MEMO_TOKEN`,
  `AF_SCHEDULE_TOKEN`, `AF_MCP_TOKEN`, `AF_DOCS_TOKEN`, `AF_ENGINE_ISSUE_TOKEN`,
  `AF_GIT_OAUTH_TOKEN`, `AF_AWS_PROFILES_TOKEN` and `AF_BRANCH_RULES_TOKEN` (plus `AF_ROUTE_ISSUE_TOKEN` when `AF_PROVIDED_ROUTES=1`), with `AF_CP_BASE_URL`. Each is
  deterministic (an HMAC of the membership id under a key derived from the master key,
  or from a random key kept in `WS_DATA` when there is none), so re-injecting it on
  every start changes nothing, and **each opens its own endpoint only**: a leaked memo
  token cannot read the tenant's MCP secrets. None is injected without
  `PUBLIC_BASE_URL`, because that is the address the container reaches the CP at.

## 3.5 The MCP server

The design and the decision are
[decisions/0006](../decisions/0006-mcp-unified.md). `/mcp` is only registered when
`AF_MCP_ENABLED=true`.

- **Transport** is the minimal Streamable HTTP form: JSON-RPC 2.0 over POST — single
  and batch — answered as JSON, with no SSE. It serves both protocol eras: the
  stateless 2026-07-28 revision (`server/discover`, the version in each request's
  `_meta`) and the older `initialize` handshake. **The edge must pass `/mcp` through
  with its bearer intact.**
- **Authentication is a personal access token**, issued from the Console and stored only
  as a hash ([06](06-data.md)). Its scope (`read` or `write`) is fixed at issue and
  capped by the issuer's own; **the role is resolved live on every call**, and the
  tenant is fixed by the token rather than supplied by the client.
- **Member tools** (`memberTools()` in `internal/mcpsrv/mcp.go`) — observe and drive
  your own sessions (list, status, output, send, create, stop, resume), cleanup and its
  archives, usage, repositories and models, and the memo queue. Their purpose is "let
  the Claude on my laptop drive my remote sessions".
- **Admin tools** (`adminTools()`) — read (workspaces, usage, sessions, the audit log,
  egress statistics and allowlist) and write (stop a workspace or a session, set a
  member's quota, propose an allowlist change). A caller who is `super_admin` or the
  tenant's `tenant_admin` sees them; writes are recorded in the audit log with
  `actor_kind=mcp`.
- **The dangerous tools** (key rotation, recreate, stopping idle workspaces in bulk) are
  not planned: nobody has asked for them, and letting an agent do these needs a decision
  of its own before anything is built ([decisions/0006](../decisions/0006-mcp-unified.md)).

## 3.6 The memo queue

Memos you accumulate and send to a session in one go (the tables are
[06](06-data.md)).

- **The CRUD of memo text and categories is CP-only**: it needs a membership and
  nothing else, so **it does not start a workspace** — you can add and organise memos
  from another device while yours is stopped. Grouping is two levels, repository ×
  category.
- **Image attachments live in the container**: a memo stores references, and
  `POST /api/memos/paste-image`, `GET /api/memos/images/{file}` and
  `POST /api/memos/images/gc` are relayed to the agent.
- **Flush** (`POST /api/memos/flush`) takes a list of ids (one representation covering
  "the whole repository", "a category" and "these ones"), joins them into a single
  message under category headings, sends it to the target session's input **exactly
  once**, and stamps them as sent. The send needs the agent, so the flush resolves the
  runtime.
- **The in-container operator** reaches the same handlers under `/internal/memos` with
  `AF_MEMO_TOKEN`.
- **Retention**: sent memos are kept for 7 days and swept lazily when the list is
  fetched, rather than deleted on send.
- **Queues** ([decisions/0137](../decisions/0137-message-queues.md), step 1a): a category
  with settings (`GET /api/memo-queues`, `PUT` / `DELETE /api/memo-queues/{categoryId}`)
  is a queue, and a `PUT` is refused while the tenant switch
  (`/api/admin/tenants/{slug}/memo-queues`, off by default) is off. Turn it on only after
  every CP runs this binary ([06](06-data.md)). A queue's memos are messages with a
  state. The face a request arrived by —
  the Console, the token face, `/mcp` — is fixed where the route is registered and decides
  the provenance and the entering state (`pending` only for the member's own note through
  the Console, `held` otherwise). `POST /api/memos/release` moves held messages to
  `pending`, all or none, each with a conditional write on its own row that also requires
  the content hash the card showed. Settings and release are Console only; on the token
  face and `/mcp` a message in a queue is not flushed or deleted, an edit of a released one
  puts it back to `held`, a queue is not renamed, merged or deleted, and a create into a
  queue is bounded (16 KiB, no `[agent-fleet:` prefix, an identical waiting message
  answered instead, 30 a minute).
- **Deliveries** (decisions/0137 §6, step 1c): a delivery is one fire of a queue, and
  every delivery that is not closed occupies one of the queue's `maxActive` slots and
  counts toward a member-wide limit of 4. `POST /api/memo-queues/{categoryId}/fire`
  (Console only) reserves a slot and claims up to `batch` of the oldest `pending`
  messages (empty `sent_at`, no `send_token`, a release hash still equal to the content
  hash; one that is not goes back to `held`) in one `MemoTx`, and answers 202 with the
  delivery in `claimed`. It is refused with nothing pending, no free slot, the tenant
  switch off, no instruction, the `max_sessions` quota (running sessions plus the
  deliveries not yet sent), or a message the Agent would refuse (`memo_queue_item_invalid`:
  a text over 64 KiB, more than 20 attachments, a path over 4 KiB). `enabled` does not
  gate it. The quota is checked again in the move to `sending`, under the membership lock,
  so two sends of one member cannot both pass it. The send runs on the scheduler's
  next tick (§3.7): wake and wait as a schedule does, reserve the durable Agent key
  `q_<delivery>`, move the delivery to `sending` with a conditional write on its state,
  and create the session with `source: "queue"`, `queue_delivery`, `idempotency_durable`
  and a new worktree off `~/repos/<repo>`. On the answer the delivery is `open` with the
  session's name and its messages `delivered`. An answer without the create's outcome (the
  Agent's refusal of the body, or a proxy's error) is settled at once by the abandon
  answer below. `GET
  /api/memo-queues/{categoryId}/deliveries` lists the newest 50. The session row carries
  `queueDelivery`, and a memo the delivery that claimed it (`deliveryId`).

- **Typed references** ([decisions/0137](../decisions/0137-message-queues.md) §4, step 3, Control Plane half). A message
  may carry a pointer to a work item, never a copy of it. This is the contract the Agent (step 3b) and the Console
  (step 3c) are built on.
  - *Wire.* `reference` is an object `{"provider": "github", "key": "acme/app#12", "url": "https://github.com/acme/app/issues/12"}`,
    absent when there is none. It is read on `POST /api/memos` and `POST /internal/memos` (create), on the `PATCH` of
    both (absent leaves it, a full object sets it, `{}` or all three fields empty clears it, `null` reads as absent),
    and it is in every memo of `GET /api/memos` and `GET /internal/memos`. `/mcp`'s memo tools do not take one in this
    step. A message that has a reference is a `text` message: its `body` may be empty only when a reference is set
    (images alone are as before). Storage is three columns, `memo.ref_provider`, `ref_key` and `ref_url`, with `''`
    for none (migrations 0138 and Postgres 0123).
  - *Validation* (`400 memo_ref_invalid`). All three or none. `provider` is lower-cased and must be one the work items
    know (`github`, `jira`, `bitbucket`). `key` is `owner/repo#N` for `github` and `bitbucket` (a bare `#N` is
    refused) and `PROJ-12` for `jira` (upper-cased), at most 200 bytes. `url` is http or https with a host, no
    whitespace, at most 2048 bytes. What is stored is the normalised form.
  - *State and provenance are the face's, as for any message* (§3): the Console enters `pending`, the token face
    and `/mcp` enter `held` and wait for the release press. A reference-only message from a session gets no exemption
    (ADR open question 2 stays open). The release hash (`memoContentHash`) covers the reference, and a message
    without one hashes exactly as before. So a change of the reference from the token face or `/mcp`, setting,
    changing or clearing it, puts a released message back to `held`, as a change of the body does, and the Console's
    own change keeps `pending` and re-records the hash. A pending row also keeps `release_ref_*` copies under a
    CHECK, like the rest of the released content.
  - *One open message per (queue, provider, key).* Open is `held`, `pending` and `claimed`: a message that can
    still be fired from, or is being fired in. `unconfirmed`, `delivered` and `superseded` are closed (an
    `unconfirmed` message is never fired by itself, and the person sees it), so the same item can be added again.
    The keys of `github` and `bitbucket` compare without regard to case. It is enforced in the transaction behind
    the member's lock, where every memo write of a member already serialises (so it holds across Control Plane
    processes on SQLite and Postgres), and a partial unique index `idx_memo_open_ref` over (membership, repo,
    category, provider, key) for open rows is the second guard for a writer that skips the check.
    - *Create* of a reference that is already open in the queue is not an error: the answer is the existing message,
      `200` instead of `201`, with `"existing": true`. Nothing is added, and nothing is counted against the rate
      limit of the token face. Repeating it is safe.
    - *An update* that sets or changes a reference, or moves a message into a queue's category, onto an open one is
      `409 memo_ref_duplicate`, the message says which message holds it.
    - *A category that becomes a queue* while it holds two notes that would both be open with one reference, and a
      *rename that merges* categories where the moved rows collide with the survivor's (or with each other), are
      refused whole with the same `409 memo_ref_duplicate`, and nothing moves: which of two notes to drop is the
      member's choice. Notes already sent become `delivered` and do not count. A restore of a message to `pending`
      (a delivery that was not sent returns its claimed messages) cannot collide, because `claimed` is open.
  - *The fire.* The item the Agent's `POST /sessions` receives in `queue_delivery.items[]` gains
    `"reference": {"provider","key","url"}` (absent for a message without one; `body` may be `""`). When the items
    of the delivery carry exactly one reference between them, the create also carries
    `"work_item": {"provider": "github", "key": "acme/app#12"}`, the object the Agent's `session_create` already
    takes from a launch from a work item (the item's title, type and labels are not kept, so they are not sent), so
    the branch name and the link follow the work-item rules. With none, or two or more, there is no `work_item`.
  - *The marker.* `GET /sessions` answers `"queueRefs": 1` when the Agent composes the prompt for items that carry a
    reference (a number apart from `queueReport`, which stays at 1). A delivery that holds a reference is never
    sent to an Agent without it. Until step 3b every Agent lacks it. The send asks the Agent after the wake and
    before the key is reserved: without the marker the delivery closes as `not_sent`, the messages go back to
    `pending` where they were, and the queue's `noRefsAt` is set. While `noRefsAt` is set a fire by itself leaves
    the messages that carry a reference where they are and fires the rest of the queue, a press of "fire now" tries
    again, and the queue's `waiting` is `{"reason": "no_references"}` when only references wait. The dispatcher
    clears `noRefsAt` when a running workspace's Agent sends the marker. `noRefsAt` is in the queue's JSON beside
    `noReportAt`. A refused send backs the queue off like any not sent delivery.
  - *Hand-sent text.* A reference in a message sent by hand is written as a line `作業項目: <key> (<url>)` by
    `buildFlushMessage`. The Console's own composer (`composeMemoMessage`) is step 3c's to match.

## 3.7 Background jobs

All are goroutines inside the CP. Intervals come from the environment, and `0`
disables.

- **Message queue deliveries** ride the scheduler's tick (`AF_SCHEDULER_INTERVAL`), so
  `AF_SCHEDULER_INTERVAL=0` also stops queue sends. Each tick first settles, within 60
  seconds: a `claimed` delivery older than a fire's budget (the wake timeout plus 10
  minutes) closes as `not_sent`; a `sending` one that old is settled by the Agent's
  `POST /sessions-idempotency/{key}/abandon` (200 open, 202 keep, 404 `not_created` close
  as `not_sent`, 503 look for a session with that `queueDelivery`: found open, not found
  close as `unknown` with its messages `unconfirmed`, no answer keep); an `open`
  one is decided by the **occupancy rule** (below). While the workspace is stopped a
  `sending` delivery keeps its slot. A running workspace whose Agent does not answer, or
  one starting or stopping, closes nothing. Then at most two queue sends share the tick's four
  parallel fires with the schedules. Every move of a delivery's state is a conditional
  write on that state, so of a slow sender and the reconciliation only one acts.

  **The occupancy rule** ([decisions/0137](../decisions/0137-message-queues.md) §6, the
  table). For a workspace whose Agent sends `queueReport: 1` on `GET /sessions`, an `open`
  delivery keeps its slot while any session of its tree (its session, and every session
  with `origin == "session"` whose `originSession` chain leads to it, at any depth; the
  walk keeps the names it has seen, so a cycle ends it) is held: a machine works
  (`machine`), a person has to act, also stopped with the interaction carried (`person`),
  a usage limit is waited for, `limited` or a booked `rateLimitResumeAt` (`limit`),
  `wakers` is not zero (`wakers`), or `wakersUnknown` is set or a state is not understood
  (`unknown`). An archived session of the tree holds nothing. The delivery closes `clean`
  on the second read in a row that finds none of these, and `aborted` when its session is
  archived or gone from the Agent's list. Each read is kept first (`held_by`,
  `held_session`, `held_seen_at`, `idle_ticks`, `progress_seen_at`), as one conditional
  write behind the membership lock, with the count of clean reads worked out from the
  stored row. Each read is numbered by the database before it begins (`read_gen`), and a
  read with an older number than the kept one (`held_gen`) is refused, so no wall clock
  decides the order; the close is made in the same transaction. A watch made between two
  reads, or a newer read another Control Plane kept, defeats it. **While the workspace is stopped, or the Agent does not
  answer, only a report that was read changes the answer**: a delivery last seen held
  stays open whatever the mirror says, and one last seen with nothing holding it closes
  from the mirror on a second tick, unless the mirror shows its session running or an
  interaction carried. An Agent that does not send `queueReport` (or sends another
  number) keeps the rule that needs no report: archived, deleted, stopped with nothing
  carried, or idle on two consecutive ticks, and nothing is ever retired for it.

  **Closing stops.** The clean close sets `retire_pending` in the same transaction. Each
  tick then calls `POST /sessions/{name}/retire` for the closed deliveries that have it,
  while their workspace is running and answers with `queueReport`. The Agent's own word ends it,
  stored in `memo_delivery.retire_result`: `complete: true` (`confirmed`), the root `released`
  (a person started the session after the retire began, so it is theirs now), a member below
  the root released while everything else is stopped (`done`), the session no longer listed
  by a successful report (`gone`), or 409 `retire_not_root` (`fault`, logged and not repeated:
  a delivery's session is always a root). A 404 is never a confirmation, and a 503
  `retire_wait_cancelled` or a timeout is repeated on a later tick (the Agent runs one retire
  per root at a time, so the call can wait). It never wakes a stopped workspace, and a repeat does no harm.
  A session that works again is retired like any other (a watch that fired again after a
  failed retire looks the same), and no time bound ends the retire. A person who resumes the
  session between our read and the Agent's start of the retire is protected by the Agent: the
  envelope's `actMark` is kept with the report (`memo_delivery.act_mark`, written by
  `ObserveDelivery` with the rest of it) and every retire is sent with `{"ifNoActSince": mark}` of
  the report it was decided on: the kept report at a clean close (a running workspace's read, and
  the kept report of a stopped one) and the kept report at the first attempt of an overdue retire.
  That mark is frozen in `memo_delivery.retire_act_mark` (set by the close, or by the first overdue
  attempt) and every unconfirmed retry sends the same one, because an overdue delivery stays open and
  keeps overwriting `act_mark` with newer reads. The overdue freeze is one conditional write on the
  generation of the report the decision was made on (`retire_gen`; 0 means not frozen, and a frozen
  mark of 0 is a frozen "no mark"): if a newer report was kept first nothing is sent and the next
  tick decides again. Progress clears it, and the record of an attempt's completion is conditional on
  the same generation. A session a
  person started or typed into after that read comes back `released` (root) or `done` (below the
  root), which end the pending retire as before. An Agent that sends no `actMark` gets no body.
  The envelope's `actIncarnation` (the identity of the Agent's counter run that handed the mark out,
  #4265) is kept with the mark (`memo_delivery.act_incarnation`) and frozen with it
  (`retire_act_incarnation`, cleared with it by progress), and the retire body carries it as
  `{"ifNoActSince": mark, "actIncarnation": id}`; none stored (an older Agent, or a row from before the
  upgrade) sends none, as before. The Control Plane only stores and sends the pair: the frozen pair stays
  frozen across an Agent restart, as the mark did, and the Agent judges each session against it (the
  act of a person recorded in the Agent's current run is after any list of an earlier run). Two Control
  Planes on one database, one older: the older writes `act_mark` and not the identity, so the pair can be
  mismatched, which the Agent reads by the same rules (at worst the mark is compared by number, as before).
  An `aborted` close retires nothing.

  **Overdue.** A delivery open longer than its queue's `maxTaskHours` (1–72, default 12)
  is marked `overdue_at` and the member gets one notification (kind
  `memo-delivery-overdue`, `EventID` `memo-overdue-<delivery>`, so two Control Planes tell
  it once), for every delivery whether or not its Agent reports. Overdue frees nothing.
  When a report is read and no session of the tree showed progress (a busy row whose
  progress is fresh, or a background job younger than `busyProgressLapse`) for an hour, the
  tree is retired, repeated each tick until the Agent confirms (`retired_at`; progress seen
  later clears it). The slot then goes by the ordinary rule once the report says nothing
  holds the tree; a session waiting for a person stays occupied by design. The first
  turn of the delivery's session (`queueTurn`) is copied onto the delivery when it first
  appears. The delivery DTO carries `heldBy`, `heldSession`, `heldSeenAt`, `overdue`,
  `overdueAt` and `firstTurn`.

  **Fairness of the passes.** `settle` closes stale claims and collects the sends first (no
  workspace read), then reads members least recently tried first (`memoFairOrder`, in memory,
  per process); the retire pass has its own order and the last third of the same 60 second
  budget (`memoRetireReserve`), so the passes together do not delay the tick's fires. A member
  that never answers goes last on the next tick; the tick count to reach everyone depends on
  how slow the workspaces are (ADR 0137, note of #4245).

  **The dispatcher** ([decisions/0137](../decisions/0137-message-queues.md) §6 and §7; it
  runs after the settle pass, in the room the settle's sends left of the tick's two queue
  slots, within a 45 second budget). It fires queues by themselves through the same
  `memoReserveAndClaim` as "fire now", under the membership row's lock, so two Control
  Planes never claim more than the free slots; every condition is read from stored state,
  none from memory. A queue fires when its tenant switch is on, it is `enabled`, not
  `paused`, open, has `pending` messages, and no session of the member that waits for a usage
  limit (`held_by = limit`) belongs to a queue of the same agent kind. **Open**: always, or a
  window (`openCron` matched within the last `openMinutes`, in the IANA zone `tz`, UTC when
  empty), or a threshold (`openAfter` pending messages with nothing added or released for a
  minute, then `draining` is kept in the row until nothing is pending). **Caps**:
  `maxFiresPerDay` (default 20, 1–200) counts the deliveries created in the last 24 hours,
  read inside the claim, wakes made only to settle included. **Order**: the member whose
  oldest pending message waited longest, and in a member the queue with the oldest message,
  one fire per member per round. **Back-off**: deliveries that closed as not sent (the Agent
  refused, the wake failed, a quota held) make the queue wait, 5 minutes doubling with each
  one in a row up to 4 hours, read from the queue's newest deliveries. **Circuit breaker**: the last two deliveries with a
  recorded first turn, created since the queue was last resumed (`breaker_after`), that
  both failed, were aborted, or ended within a minute without a clean end pause the queue
  (`paused`, `paused_reason`) and notify once (`memo-queue-paused`);
  `POST /api/memo-queues/{id}/resume` lifts it. A pause stops fires by themselves, not
  "fire now". **An Agent that does not send `queueReport`** is not fired into by itself: a
  running workspace is read and `no_report_at` set (shown in the queue DTO); a stopped one is
  not read, and a delivery fired by itself (`memo_delivery.auto`) checks after its wake and
  returns its messages when the report is missing. **Why a queue with notes waiting starts nothing**:
  `GET /api/memo-queues` adds `waiting: {reason, until?, detail?}` per queue (absent when a run can
  start, when nothing waits, and from an older CP). It is not stored: `memoWaitingFor`
  (`memo_dispatch.go`) is the one function that decides whether a queue may fire, called by the
  dispatcher behind the membership lock and by the list through `MemoRead`, which takes no lock.
  The reasons are checked in this order, and the first that holds is reported: `tenant_off`,
  `disabled`, `no_report`, `paused`, `window_closed` (`until` = the window's next start in the
  queue's zone), `below_threshold` (`detail.needed`, `detail.waiting`), `quiet_minute` (`until` =
  the end of the quiet minute), `daily_cap` (`until` = when enough runs have left the last 24
  hours for the count to fall below the cap), `backoff` (`until` = the end of the back-off),
  `usage_limit` (`detail.agentKind`), `no_place` (`detail.by` = `category` or `member`). A lack of
  the Agent's session quota is not one of them. **Wakes made only to settle**: when every
  slot of a queue with pending messages is held by a `sending` delivery, or an overdue one
  waiting for its retire, in a stopped workspace, the workspace is woken (in one of the
  tick's fire slots, at most once in ten minutes per queue and once per tick per workspace,
  also when the tick's sends filled the queue room), recorded as a closed delivery with
  outcome `wake` that counts as a fire of the day and is not listed. **Notifications**, each
  once through its `EventID`: `memo-queue-paused`, `memo-queue-waiting` (every slot held by a
  session that waits for a person, per set of deliveries) and `memo-delivery-unconfirmed`.
  **Rechecks behind the lock**: the claim of a fire by itself, and the record of a settle wake,
  take the tenant's row (turning the switch off waits for them) and read the switch, the queue
  (`enabled`, `paused`, the cap), the not-sent back-off and the usage limit again inside the
  membership transaction, so what the tick read earlier cannot start a session. The usage limit
  reads `memo_delivery.limit_kinds`, the kinds of every session of the tree that waits for a
  limit (kept apart from `held_by`, which names only the strongest reason), so a limited parent
  with a working child still holds its kind, and a queue changed to another kind is not held by
  the old one. The breaker decides its pause in a membership transaction too, from the queue's
  current `breaker_after`, and a resume runs in one, so a pause decided on failures from before
  a resume cannot land after it. `draining` ends in the transaction that leaves nothing pending
  (every `MemoTx` checks before it commits), so a message added after an empty moment starts a
  new batch; a pause keeps it while messages are left, and the drain goes on with the resume.
  **Bounds on the wakes**: at most two settle wakes are started per tick (the tick waits for
  them), and one workspace is woken at most once in ten minutes whatever queue asks (counted per
  member, inside the membership lock). A batch that ended a drain by claiming the last pending
  message and then returned as not sent puts the queue back to draining (`memo_delivery.drain`),
  so it does not wait for a threshold it had passed. The first turn of a session that showed
  after its delivery closed (archived or stopped first) is copied onto the closed delivery for a
  day after the close, so the breaker counts it; every delivery reached is stamped (`turn_tried_at`) before it is tried, with a context that does not inherit the pass's deadline, and the least recently tried goes first (the pass has a 20 second budget of its own), so
  fifty that never fill cannot keep the rest from being read; a session deleted before its turn
  was seen leaves nothing to read, and that delivery is never counted. The drain mark is set
  by the common claim, so a manual fire's returned last batch restores the drain too. **Idle count and `gone`**: a second
  clear read counts only when the stored row was itself clear (`held_by` empty), so an older
  Control Plane's idle count never closes on one clear read; and a pending retire ends as `gone`
  only on a read made after the close (a fresh read when the tick's cached list lacks the root).
  **Deletion guard**: an enabled queue blocks deleting its repository like an enabled
  schedule (`schedule_in_use`). **Mixed versions**: an older Control Plane never fires by
  itself, its settings PUT lists none of the new columns and so keeps every setting and
  state (a pause too), and a queue it creates gets today's defaults (always open, 20 a
  day). A new Control Plane's PUT replaces the settings and leaves the state alone.

- **The reaper (idle stop)** — `AF_IDLE_SWEEP_INTERVAL` (1 minute). **On by default**:
  a session idles out after an hour (`AF_SESSION_IDLE_TIMEOUT`), a session waiting on a
  person — a question, a plan approval, a permission — after `AF_INTERACTION_IDLE_TIMEOUT`
  (the session value unless set), and a workspace after two hours
  (`AF_WS_IDLE_TIMEOUT`). Those are deployment defaults; a tenant's limits override
  each, `0` included. Four tiers:
  - **Tier 1** halts an idle, unattached session — every kind but `shell` and `ssm`,
    whose halt would kill the running job. It is resumable.
  - **Tier 2** stops a workspace with no presence (§3.3), no session that holds it, no
    repository import or image job running, and no recorded activity (§3.2) within the
    timeout. A
    `starting` workspace is never touched. The reaper publishes what it saw, so the
    admin screen explains "why won't it stop" with the reaper's own answer.
  - **Tier 3** (ecs-ec2 only) hibernates the home of a workspace stopped for longer than
    `AF_ECS_EC2_HIBERNATE_AFTER_SEC`: the EBS volume is snapshotted and deleted, and the
    next start restores it. Off by default.
  - **Tier 4** (ecs-ec2 only) copies each home to another Availability Zone every
    `AF_ECS_EC2_BACKUP_EVERY_SEC`, whatever the workspace is doing. Off by default.
- **The usage sampler** — `AF_USAGE_SAMPLE_INTERVAL` (5 minutes) adds occupied seconds
  to daily and hourly buckets for each running workspace, which also feeds the uptime
  heatmap. With bring-your-own model credentials, **the operator's cost is occupancy,
  not tokens** — which is what this measures. `0` turns it off. The same walk enforces the
  ceiling on `starting` (`start_deadline.go`): a workspace still `starting`
  `AF_WORKSPACE_START_DEADLINE` (30 minutes) after the later of its last Start and this CP's
  first sighting, with no task running (the adapter's `runtime.TaskCounter`, or else the
  agent answering), is stopped under the lifecycle fences, like an explicit stop. An adapter
  with background launch work (`runtime.LaunchBudgeter`, ecs-ec2) raises the limit to its
  own budget. The stops run off the walk, at most two at a time and each within two
  minutes. Free workers go to the overdue workspaces whose last attempt that got past the
  fences is oldest, so all of them are reached in turn; a busy fence does not count as an
  attempt, and that workspace is first in line on the next sample. Only a launch that
  cannot converge gets there — a task ECS refuses to place, for one. `0` in
  `AF_WORKSPACE_START_DEADLINE` turns it off, and so does switching the sampler off.
- **The cloud-cost poller** — where the runtime has a bill (the AWS targets), it reads
  Cost Explorer every `AF_CLOUD_COST_INTERVAL` (6 hours) over a trailing
  `AF_CLOUD_COST_WINDOW_DAYS` (7) and attributes spend per member by cost allocation
  tag. On `docker` and `native` it does nothing, and there is no cost screen. `0` turns
  it off.
- **Git GC** — `AF_GIT_GC_INTERVAL` (24 hours) runs `git gc --auto` on the internal bare
  repositories and prunes orphaned LFS objects older than `AF_LFS_GC_GRACE` (14 days),
  so it cannot race a push in flight. Ledger rows whose object never landed on disk are
  dropped after the same grace period. It runs **sequentially, to protect a shared host's
  RAM** ([91](91-internal-git.md)). `0` turns it off.
- **The scheduler** — `AF_SCHEDULER_INTERVAL` (1 minute) fires due schedules, spread by
  a per-schedule jitter (`AF_SCHEDULE_JITTER`, 2 minutes). A fire wakes a stopped
  workspace without the CLI self-update, waits up to `AF_SCHEDULE_WAKE_TIMEOUT` (the
  300-second boot budget) and holds a keep-alive for `AF_SCHEDULE_SETTLE`. `0` turns it
  off: nothing fires, and the Console hides the Schedules section.
- **The audit sweep** — `AF_CLAUDE_AUDIT_INTERVAL`, opt-in, off by default. What claude
  does inside the container does not pass through the CP's proxy and is therefore
  invisible; the agent → CP direction is deliberately closed, so **the CP pulls
  instead**, reading each running claude session's transcript and auditing its writes,
  edits and commands (`actor_kind=claude`). It advances a per-session cursor, and **a
  session seen for the first time only sets a baseline** — it does not retroactively
  audit the past.
- **The ecs-ec2 pool** — a drift sweeper (`AF_ECS_EC2_SWEEP_SEC`, 5 minutes) re-derives
  slots, volumes and owner tags from AWS and finishes whatever a crashed CP left
  half-done, and the golden-snapshot auto-bake (`AF_ECS_EC2_GOLDEN_AUTOBAKE`, on)
  rebakes the snapshot new homes are seeded from when the workspace image changes. It
  runs whether or not idle-stop is on.
- **The engines** — each role's controller, the engine-table reload and the borrowed
  catalogue poll (§3.9).
- **Metrics are on demand, not a job.** When the CP shares a host with the workspace it
  reads `/proc` and the container's cgroup directly; otherwise (ECS) it asks the agent.
  Host-wide statistics are limited to a deployment administrator, so one tenant cannot
  infer another's load.

## 3.8 The CP's half of egress control

The design and the staged rollout — log-only → allowlist → enforce — are
[07 §7.8](07-security.md). What lives in the CP:

- **An egress-proxy subcommand** — `control-plane egress-proxy` runs the same binary as
  a forward proxy (FQDN-based, no TLS interception, `AF_EGRESS_LISTEN`, `:3128`). A host
  outside the allowlist is blocked only when enforce is on; loopback, link-local (the
  cloud metadata address included) and unspecified destinations are refused in every
  mode.
- **An egress-agent subcommand** — `control-plane egress-agent` is the small agent on the
  egress gateway of ADR [0122](../decisions/0122-egress-gateway.md) (decision 4,
  `internal/egressagent`). It writes Squid's allow file and mode include from the policy
  and reloads Squid on a change (and once at start), and ships Squid's log to ingest from a stored file
  offset, one event per line with the line's position as its `id`. The Squid lines it
  expects and its log format are in the package doc.
- **Policy distribution** — `GET /internal/egress/policy` returns the effective
  allowlist and mode to the proxy and the gateway agent.
- **Ingest** — `POST /internal/egress` (`AF_EGRESS_TOKEN`) takes observed events into a
  daily aggregate, with would-block entries de-duplicated per day and host and also
  recorded to the audit log. An event that carries an `id` (the egress gateway's, ADR
  [0122](../decisions/0122-egress-gateway.md)) is also kept raw with its `at`, `source` and
  `dest`, and is stored and counted once however often it is resent.
- **Retention** — raw events are kept `AF_EGRESS_EVENT_RETENTION_DAYS` days (default 30; `0` =
  forever). A purger (one CP at a time, under a CP lease, every 10 minutes) deletes older events
  and the closed `workspace_address` rows that ended before them, in batches of 5000 rows;
  an open row is never deleted. `egress_daily` is not touched. Because dedup on `id` only holds
  while the row exists, ingest refuses to store or count an event whose `at` is older than the
  window, or older than the **purge floor** (logged, still answered 204): a resend of a purged
  event cannot be counted twice. The floor is the newest cutoff any purge has deleted up to, kept
  in the database (`cp_counter` row `egress-event-floor`), raised before each delete and read by
  `RecordEgressEvent` in the same transaction, so it holds for every CP, whatever its clock or
  its retention setting, and it never falls: lengthening the retention or setting it to `0`
  does not let a purged event back in. An address row is deleted only after the event delete
  finished without error.
- **Archive (optional)** — with `AF_EGRESS_ARCHIVE_URL` set (`s3://bucket/prefix`,
  `gs://bucket/prefix`, or `file:///absolute/dir` for a mounted volume; empty = off), the purger archives before it
  deletes. Only whole UTC days are archived, so the cutoff becomes the UTC midnight at or before
  now minus the retention (the retention is effectively up to 24 hours longer, and the floor is
  raised to that midnight, before the archive is read). One object per day,
  `<prefix>/egress/yyyy=YYYY/mm=MM/dd=DD/events-YYYY-MM-DD.jsonl.gz`: gzip JSON lines, a `header`
  line (schema version, day), the `address` rows whose interval meets the day, the `event` rows
  in `(at, id)` order, and a closing `manifest` line (row counts, id and time range). The manifest
  is last because the counts are known only after the rows were streamed (a day is millions of
  rows, paged 5000 at a time and staged in a temporary file, never held in memory); an object
  without it, or whose counts disagree, is damaged. An object is complete or absent (S3: a single
  PutObject confirmed by a HeadObject length check; GCS: one upload finalized under
  `ifGenerationMatch=0`, sent with its CRC32C and compared with the stored length and CRC32C;
  local: temp file, fsync, rename), so a partial
  object cannot exist. A day whose object exists is read back and verified (header, counts,
  gzip checksum) and never rewritten: after a crash between write and delete, or a delete cut off
  midway, the retry skips the write and deletes, and a rewrite could not replace the full day
  with the part that is left. A damaged existing object (zero bytes, truncated, wrong header)
  stops the purge at its day and is never overwritten by the CP; an operator removes it.
  **A day that fails to archive (or is not confirmed) is not deleted, nor is any later day.**
  Earlier days are, and the next run (10 minutes later) retries. Address rows are not closed to
  writers by the floor (one can arrive, or change its end, after its day was written), so the
  closed rows to delete are written as an address supplement object
  (`<prefix>/egress/addresses/yyyy=/mm=/dd=/addresses-<sha256>.jsonl.gz`, kind `addresses`)
  immediately before, and exactly those row versions are deleted (a row that changed since is
  kept for the next run). A day object holds the address rows meeting the day as they were then, so read-back must
  normalise them (below). An existing day object is also merged against the events the database
  still has for that day (both in `(at, id)` order, ids compared bytewise): one that lacks any of
  them, or holds different content, stops the purge, so a well-formed partial day cannot
  authorise a delete. A day with only some events left (a delete cut off midway) passes.
  Role permissions: `s3:PutObject` and `s3:GetObject` on the prefix, and `s3:ListBucket` on the
  bucket: without it S3 answers a HeadObject for a missing key with 403 instead of 404, and the
  archive stops at the first day. GCS: `roles/storage.objectCreator` and
  `roles/storage.objectViewer` on the bucket (create and get, never list, delete or overwrite),
  through Application Default Credentials only (Workload Identity on GKE; no key file is read
  from configuration). The sink never replaces a key: a Put on an existing one fails the
  precondition and is always an error, even when the object there has the same length and
  CRC32C (a retried upload whose first attempt landed looks like that, and so does a CRC32C
  collision); the next run finds the key with `Exists` and the archiver verifies the object
  against the database as for any other sink. Local: the file and every directory above it, up to the filesystem root, are fsynced on every Put and every
  read-back of an existing object, so a failed fsync is retried by the next run and a readable
  file is never taken as proof it is durable.
  A bad URL stops the CP at startup rather than deleting without an archive.
  **Reading back is offline:** download the day objects and the address supplements, collapse their `address` rows to one
  per `(ref, address, from)` with the smallest non-zero `until` (0 = open; the store only ever
  shortens an end, and a stale open copy in a day object would keep matching events after the
  end a supplement recorded), and attribute each event to those rows by source and time (`from <= at < until`, `until` 0 = open), as the Console does.
  Archived events are never re-ingested: ingest still refuses an event older than the window or
  the floor, so the archive and `egress_daily` cannot double count.
- **Attribution** — when `AF_EGRESS_TOKEN` is set, a recorder (one CP at a time, under a CP
  lease) asks each workspace's runtime every `AF_EGRESS_ADDRESS_INTERVAL` (default 60s) which
  addresses it holds: the ECS task ENI,
  the pod IP, or the container's address on the per-user network. It keeps them as time ranges.
  A docker or Kubernetes Stop hands the address over just before it removes it, because nothing
  is left to read afterwards. An ECS task that DescribeTasks answers MISSING stays open for 15
  minutes before it is closed.
  `GET /api/admin/egress/events` joins each raw event to them on address and time and reports
  the event as attributed, ambiguous, unattributed, or infrastructure (a source inside
  `AF_EGRESS_INFRA_CIDRS`).
- **The admin API** — `/api/admin/egress*`, `super_admin` only: the statistics, the
  allowlist (active, proposed, retired) and the log-only / enforce switch.
- **The member face** — `GET /api/egress/check` says whether a workspace can reach a
  host, and `POST /api/egress/propose` files a *proposed* entry that a deployment
  administrator still has to approve.

The container side is only wired when `AF_EGRESS_PROXY_ADDR` is set, which injects the
proxy environment into every workspace on every target (what each target takes, and what stays
out of the proxy, is [07 §7.8](07-security.md)); **the default is off, and nothing changes**.

## 3.9 Self-hosted engines

What an engine is, and which target can have which, are [01 §1.3](01-architecture.md)
and [ref/deploy-targets](../../guide/ref/deploy-targets.md). The CP owns the engines in
the deployment's table, and **a workspace never talks to one of them directly**
([decisions/0071](../decisions/0071-self-hosted-inference-engines.md)). The exception is
a member's own llama.cpp server, set up as an lcpp connection: the agent dials its URL
itself, the CP is not involved, and when it is set it wins over the deployment's engine
([08](08-integrations.md)).

- **The engine table** — one row per role (`llm`, `image`, `comfy`), with a provider
  (`llamacpp`, `comfy`, `openai-compat`) and a lifecycle: this deployment's own ECS
  service, `external` (a URL nobody here starts), or `remote` (another deployment's).
  It comes from `AF_ENGINES_SSM_PARAM` on AWS or `AF_ENGINES_JSON` inline; on `docker`
  and `native`, `AF_LLM_URL` and `AF_COMFY_URL` add an `external` row
  ([decisions/0076](../decisions/0076-external-image-engine-on-lan.md)). The SSM form
  is re-read every 10 seconds, but only the offer ladder and the capacity provider
  apply live.
- **The model catalogue** — models are database rows, not table entries
  ([decisions/0072](../decisions/0072-engine-model-catalog.md)). The CP resolves a
  Hugging Face or Civitai source and starts an ingest task that writes to S3; the CP
  itself only reads S3. The active set is published for the engine to load.
- **The gateway** — `/engine/{key}/v1/*` (and `GET /engine/{key}/props`), registered
  only when an engine table exists. A workspace holds the issuing token
  (`AF_ENGINE_ISSUE_TOKEN`) and exchanges it at `POST /internal/engine/token` for a
  token valid for 30 days, bound to the membership, one engine key and, when the caller
  names one, one session. Where the caller names none (opencode's shared Managed daemon,
  image generation, the boot-time probes) it covers the whole workspace. That second
  token is the one a model can read: opencode receives it as `AF_ENGINE_TOKEN`. Every
  call re-checks that the membership is still live and that the tenant may use that
  engine.
- **Cold starts** — a streaming request is answered 200 at once and kept alive with a
  heartbeat; a non-streaming one is held for `AF_ENGINE_PLAIN_HOLD` (45 seconds, 75 for
  a borrowed engine) and then answered `503 engine_waking` with `Retry-After` while the
  engine keeps coming up. 45 seconds keeps the response inside the load balancer's
  60-second idle timeout.
- **Starting and stopping** — a controller per role moves the ECS service between 0 and
  1 (`off`, `on` or `ondemand`) and stops it after an idle period set per role under
  Admin → Inference engines (`AF_ENGINE_<KEY>_*_SEC` for the defaults). On `ecs-ec2`,
  when the row declares offers, the CP buys the GPU instance itself with one
  `CreateFleet(type=instant)` per offer, in the declared order, and finds its boxes
  again by tag ([decisions/0077](../decisions/0077-engine-boxes-bought-by-cp.md)). A
  Spot offer is used only after a deployment administrator accepts it
  ([decisions/0075](../decisions/0075-engine-purchase-offers.md)); instance classes are
  [decisions/0074](../decisions/0074-engine-instance-classes.md).
- **Borrowing** — with `AF_REMOTE_ENGINE_URL` and `AF_REMOTE_ENGINE_TOKEN` (a token the
  lender issues at `POST /api/admin/engines/issue-token`), the CP adopts the lender's
  catalogue every 2 minutes and buys session tokens from it
  ([decisions/0079](../decisions/0079-remote-engine-from-another-deployment.md)).
- **The APIs** — `/api/admin/engines…` for administrators, `GET /api/engines/status`
  and the `engines` stream of `/api/events` for members, and `GET
  /internal/engine/catalog` for the agent and for a borrowing deployment.
