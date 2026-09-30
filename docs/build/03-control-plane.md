---
audience: "someone changing the Control Plane"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-09"
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
  allowlist**: every relayed path is a route the CP registers (`routes.go` and the
  `register*Routes` functions beside it), so an agent endpoint without a CP route is
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
  `AF_GIT_OAUTH_TOKEN` and `AF_AWS_PROFILES_TOKEN`, with `AF_CP_BASE_URL`. Each is
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

## 3.7 Background jobs

All are goroutines inside the CP. Intervals come from the environment, and `0`
disables.

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
  so it cannot race a push in flight. It runs **sequentially, to protect a shared host's
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
- **Policy distribution** — `GET /internal/egress/policy` returns the effective
  allowlist and mode to the proxy.
- **Ingest** — `POST /internal/egress` (`AF_EGRESS_TOKEN`) takes observed events into a
  daily aggregate, with would-block entries de-duplicated per day and host and also
  recorded to the audit log.
- **The admin API** — `/api/admin/egress*`, `super_admin` only: the statistics, the
  allowlist (active, proposed, retired) and the log-only / enforce switch.
- **The member face** — `GET /api/egress/check` says whether a workspace can reach a
  host, and `POST /api/egress/propose` files a *proposed* entry that a deployment
  administrator still has to approve.

The container side is only wired when `AF_EGRESS_PROXY_ADDR` is set, which injects the
proxy environment into every workspace; **the default is off, and nothing changes**.

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
