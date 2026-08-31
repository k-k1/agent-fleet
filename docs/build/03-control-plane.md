# 03. Control Plane

English | [日本語](03-control-plane.ja.md)

Audience: someone changing the Control Plane
Source of truth: the code (this is a map and a statement of intent)
Updated: 2026-07

The CP is the only resident backend outside the workspaces — one Go binary. The
browser only ever talks to it, and **it never touches tmux or a working copy itself**:
everything goes through the agent ([01 §1.3](01-architecture.md)). This chapter is
"what lives in the CP and how it connects". The wire contracts are [05](05-api.md); the
security design is [07](07-security.md).

## 3.1 Responsibilities

- **Serving the Console** — the built bundle at `/` with `no-store`, so a deployment is
  live immediately ([05 §5.4](05-api.md)).
- **The auth gate (L1)** — with `AUTH=oauth` the CP *is* the edge: it verifies the
  signed cookie on every request, **strips any inbound identity header and re-injects
  the verified value**, and fails closed against the allowlist. The dev and proxy modes
  have no gate ([07 §7.3](07-security.md)).
- **Resolving identity and tenant** — verified email → identity; the tenant header, then
  a query fallback, checked against a membership. Provisioning an unknown identity and
  granting deployment-administrator rights also happen here (§3.2).
- **Workspace lifecycle** — one membership means one container: allocation, start, stop,
  recreate, and mirroring the state to the database. The substrate is behind the Runtime
  adapter (§3.3).
- **Relaying to the agent** — five paths: REST, SSE, terminal WebSocket, browser
  REST + WebSocket, and preview ([05 §5.3](05-api.md)).
- **The metadata store** — SQLite by default, Postgres available ([06](06-data.md)).
- **Audit** — the proxy layer (mutating calls), the admin API, MCP writes and system
  jobs all record. Where it is written is [05 §5.5](05-api.md); the design is
  [07 §7.7](07-security.md).
- **An MCP server** — exposes member and admin tools to an external client (§3.5).
- **A built-in git provider** — bare repositories over smart HTTP with LFS, hosted by
  the CP itself, **not through the agent** ([91](91-internal-git.md)).
- **Egress control** — distributing policy to the forward proxy, aggregating observed
  events, and the admin API (§3.8).
- **The memo queue** — per-membership memos and batch send. A **CP-only** feature, so it
  works while the container is stopped (§3.6).
- **The scheduler** — schedule definitions live in the CP's database and a goroutine
  evaluates cron expressions and fires them, resolving time zones (including DST) from
  an embedded IANA database. Creation and editing go through an internal endpoint used
  by the operator conversation; the Console's own endpoint is read and manage only
  ([decisions/0021](../decisions/0021-scheduled-execution.md)).
- **Notifications** — the agent's outbox is drained into the CP's store when fetched,
  and exposed as a list with read-state management (kept 7 days).
- **The tenant MCP registry** — server definitions a tenant administrator distributes to
  every member. A member's own registrations are composed on the agent side and merely
  relayed ([decisions/0031](../decisions/0031-mcp-registry.md)).
- **Background jobs** — the reaper, the usage sampler, git GC, and the audit sweep
  (§3.7).

Note that **cleanup** and **agent memory management** are *agent* features; the CP just
relays them ([decisions/0022](../decisions/0022-agent-memory-management.md)).

Which file implements what is [90-code-map](90-code-map.md).

## 3.2 The life of a request

Every public API call goes through the same front half (the authorisation principles
and error shapes are [05 §5.4](05-api.md)):

1. **The auth gate** (oauth mode only) — verify the cookie and inject the email. The
   OAuth routes, the login landing and the health check are excluded; the MCP endpoint
   (bearer PAT) and the git endpoints (basic auth) authenticate themselves
   ([07 §7.3](07-security.md)).
2. **Resolve the identity** — email → identity. An unknown one is auto-provisioned into
   the default tenant, or rejected, according to the provisioning mode.
3. **Check the membership** — the tenant header, then the query fallback.
4. **Resolve the workspace runtime** — membership → workspace row (allocating one if
   needed, §3.3) → unwrap the DEK (§3.4) → build the Runtime through the factory,
   cached in memory but with the database as the truth. **Every ingress records
   liveness** in the connection tracker.
5. **Handle or proxy** — CP-only surfaces are answered here; everything else goes to the
   agent over one of the five paths ([05 §5.3](05-api.md)).

Relaying requires the workspace to be running (stopped is a 409), **but operations with
an unambiguous intent** — creating a session, forking, starting — wake a cold workspace
first. Attaching a terminal, and anything read-only, does not auto-start. The
auto-start path waits for the agent to become reachable, **inside the ingress idle
timeout**, and returns a "workspace starting" 409 if it does not make it — the start
itself continues in the background, so the retry succeeds.

## 3.3 The manager and the Runtime abstraction

- **The manager** allocates each membership's resources once and persists them: the
  container name, a dedicated network, the home directory path, the agent port, and the
  agent token. The default tenant keeps an older, slug-free naming **for compatibility
  with deployments that already exist**. Across a CP restart the database row is the
  truth, and an existing container is **adopted by inspection rather than recreated**.
- **`Runtime` / `RuntimeFactory`** abstract the substrate, and **every call site** —
  handlers, the reaper, admin, MCP — builds through the factory. Docker and ECS are one
  profile switch ([01 §1.6](01-architecture.md), [09](09-deploy.md)).
- **Start** mounts the home and the agent config, ensures the dedicated network, injects
  the tokens and keys, and waits for the agent to be healthy. **Stop is two-stage and
  graceful**: SIGTERM, a grace period, then SIGKILL. The agent is handed a *shorter*
  grace than the outer one, deliberately, so it can interrupt the pane and let tmux exit
  before the hammer falls.
- **Connection tracking** counts long-lived connections, per-session attachment, and the
  last request time, in memory. **A workspace stays warm while anything is open**, and
  this is what the reaper reads (§3.7). A hidden browser page, and one inside its
  post-disconnect grace, deliberately do not count.

## 3.4 Key wiring at start

The cryptography itself — envelope encryption, key derivation, the limits of
crypto-shredding — is [07 §7.6](07-security.md). Only the wiring is here:

- **At boot**, the master key is hashed into the key custodian if it is set. Without it
  there is no encryption at all (development only).
- **When a workspace is resolved**, the wrapped DEK is unwrapped by the custodian (the
  first time, a legacy DEK is derived and wrapped — a compatibility point that avoids
  re-encrypting an existing store), and the plaintext DEK is injected into the container
  as an environment variable. **The agent is indifferent to the scheme and never learns
  where the key came from.**

## 3.5 The MCP server

The design and the decision are
[decisions/0006](../decisions/0006-mcp-unified.md). The endpoint is only registered when
it is explicitly enabled.

- **Transport** is the minimal Streamable HTTP form: JSON-RPC over POST — single and
  batch — answered as JSON, with no SSE. **The edge must pass the endpoint through with
  its bearer intact.**
- **Authentication is a personal access token**, issued from the Console and stored only
  as a hash ([06](06-data.md)). **The role is not frozen at issue time — it is resolved
  live on every call** — and the tenant is fixed by the token rather than supplied by
  the client.
- **Four member tools**, whose purpose is "let the Claude on my laptop drive my remote
  sessions".
- **Admin tools**, split into read and write. Writes are gated on the administrator
  roles and recorded in the audit log with an MCP actor kind.
- Still to come: the dangerous tools — key rotation, recreate — which are 📋 and assume
  confirmation plus a dry run.

## 3.6 The memo queue

Memos you accumulate and send to a session in one go (the table is
[06](06-data.md)).

- **CRUD is CP-only**: it needs a membership and nothing else, so **it does not start a
  workspace** — you can add and organise memos from another device while yours is
  stopped. Grouping is two levels, repository × category.
- **Flush** takes a list of ids (one representation covering "the whole repository",
  "a category" and "these ones"), joins them into a single message under category
  headings, sends it to the target session's input **exactly once**, and stamps them as
  sent. Only the send needs the agent, so only the send can auto-start.
- **Retention**: sent memos are kept for 7 days and swept lazily when the list is
  fetched, rather than deleted on send.

## 3.7 Background jobs

All are goroutines inside the CP. Intervals come from the environment, and `0` disables.

- **The reaper (idle stop)**. Two tiers. **Tier 1** halts an idle, unattached agent
  session past its timeout — resumable, because the transcript is on disk; a shell
  session is never halted. **Tier 2** stops a workspace with no activity at all: no open
  connections, no working or questioning session, and nothing since the last request.
  The evidence is the connection tracker (§3.3), and **a starting workspace is never
  touched**.
- **The usage sampler** adds occupied seconds to a daily bucket for each running
  workspace. With bring-your-own model credentials, **the operator's cost is occupancy,
  not tokens** — which is what this measures.
- **Git GC** runs `git gc --auto` on the internal bare repositories and prunes orphaned
  LFS objects after a grace period, so it cannot race a push in flight. It runs
  **sequentially, to protect a shared host's RAM** ([91](91-internal-git.md)).
- **The audit sweep** (opt-in, off by default). An agent's own actions inside the
  container do not pass through the CP's proxy and are therefore invisible; the
  agent → CP direction is deliberately closed, so **the CP pulls instead**, reading each
  running session's transcript and auditing its writes and commands. It advances a
  per-session cursor, and **a session seen for the first time only sets a baseline** —
  it does not retroactively audit the past.
- **Metrics are on demand, not a job.** As a host process the CP reads `/proc` and the
  cgroup directly. Host-wide statistics are limited to a deployment administrator, so
  one tenant cannot infer another's load.

## 3.8 The CP's half of egress control

The design and the staged rollout — log-only → allowlist → enforce 🚧 — are
[07 §7.8](07-security.md). Only four things live in the CP:

- **An egress-proxy subcommand**, so the same binary and image can run as a forward
  proxy alongside (FQDN-based, no TLS interception).
- **Policy distribution** to that proxy.
- **Ingest** of observed events into a daily aggregate, with would-block entries
  de-duplicated per day and host and also recorded to the audit log.
- **An admin API** for the statistics, the allowlist and the mode switch.

The container side is only wired when the proxy address is configured; **the default is
off, and nothing changes**.
