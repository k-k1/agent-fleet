---
audience: "everyone's first chapter — anyone who wants the shape of the whole thing"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-09"
---

# 01. Architecture

English | [日本語](01-architecture.ja.md)

## 1.1 What it is, and how it is delivered

A self-hosted web service that lets several members of one organisation share CLI
coding agents. Each user gets an isolated environment — a **workspace** — where git
repositories live, and drives sessions, terminals, git, files and chat from a browser
Console.

- **Delivery model**: a packaged product, self-hosted by each company. **One company =
  one deployment.** SaaS was abandoned on terms-of-service grounds
  ([decisions/0001](../decisions/0001-self-host-vs-saas.md)).
- **Scale assumed**: several sessions per person, one host or one cluster per
  deployment. The member count it is sized for is in
  [00 → Settled assumptions](00-project-context.md#settled-assumptions-v1).
- **Agent credentials are brought by the user.** Each person signs in with their own
  account ([08](08-integrations.md)).
- **The deployment target is the company's choice**: Docker on one host by default, a
  Docker-less single-user install, or their own AWS — ECS, or compose on a single VM
  ([09](09-deploy.md); what differs between them is
  [ref/deploy-targets](../../guide/ref/deploy-targets.md)). One core, with only the
  deployment layer swapped through ports and adapters (§1.6).

## 1.2 Terms

| Term | Definition |
|---|---|
| Workspace | The persistent environment for one membership (identity × tenant): a container on every target except `native`, where it is sandboxed host processes. Holds the home directory, the encrypted store and the working copies |
| Working copy | The directory of a git repository (or an SVN checkout) inside a workspace (`~/repos/<name>`) |
| Session | The logical unit of a conversation, its settings and its execution state, tied to a working copy or an arbitrary directory. It has a kind and a driver, and is **not** necessarily one-to-one with a process or a tmux pane |
| Driver | How a session is controlled. `tui` means a CLI screen inside tmux. `managed` means the agent drives the CLI through a structured API — a daemon shared by the workspace (codex, opencode), a child process per session (copilot, cursor and kiro over ACP, and muse), or code inside the agent itself (lcpp); which kind supports which is [04 §4.3](04-agent.md). The user-facing names are "execution method", "Managed" and "Terminal (CLI)" |
| Control Plane (CP) | The resident backend outside the workspaces: authentication, orchestration, relaying, persistence |
| Workspace Agent | The resident process inside each workspace. **The only thing that touches tmux, the working copies, the filesystem and the CLI agents directly** |
| Console | The browser SPA (React + Vite), served statically by the CP |
| Tenant / Identity / Membership | A department / a person / the many-to-many join between them. Workspaces are separated per membership ([06](06-data.md)) |
| Engine | A model server the deployment itself provides — llama.cpp, ComfyUI, VOICEVOX. A workspace reaches one only through the CP |

## 1.3 Three processes (Docker is the default)

```
Browser (Console SPA: React + Vite + zustand, xterm.js + browser-pane canvas)
   │ HTTPS / WSS
   ▼
[edge]  Caddy (automatic TLS, compose) / Tailscale Funnel / ALB … operator's choice (09 §9.3)
   │ on one host, passes through to the CP's loopback port
   ▼
Control Plane (resident Go process; CP_ADDR defaults to :8080, the image sets 127.0.0.1:8099)
   │  · authGate (L1) → resolve identity/membership → authorise
   │  · serves the Console bundle / REST / WS / SSE
   │  · workspace lifecycle (Runtime adapter: docker | native | ecs | ecs-ec2)
   │  · metadata store (SQLite by default | Postgres)
   │  · internal git provider / MCP / audit / egress / memos / reaper
   │  · engines: the catalogue, the gateway (/engine/<key>/v1/*) and, on ecs-ec2,
   │    the GPU instances it buys
   │
   │  relays: REST / SSE / terminal WS / browser REST+WS / preview
   │  auth: a per-workspace bearer token (AGENT_TOKEN), injected by the CP at start
   ▼
Workspace Agent (Go, inside each workspace; AGENT_ADDR defaults to :7700)
   │  · session lifecycle / driver selection and recovery
   │  · the managed drivers: a shared daemon, a child per session, or in-process (§1.2)
   │  · tmux / PTY for the terminal-driven kinds
   │  · git / filesystem / connections (the encrypted store `secrets.enc`)
   │  · chat (headless CLI) / transcript / usage
   │  · the browser manager (Chromium over CDP, pages, JPEG screencast, input)
   │  · a preview relay (/proxy/{port}) to services running inside the workspace
   ▼
the CLI agents, in tmux or under a managed driver, plus the working copies (~/repos)
```

- On `docker`, each workspace container is named per membership (`af-ws-<slug>-<key>`,
  or `af-ws-<key>` in the default tenant) and joined to a network of its own
  (`af-net-…`; `manager.workspaceNames`), so containers cannot reach each other. The
  agent's port is published on the host's loopback only, so the CP is the only thing
  that can reach it. The AWS targets draw the same boundary with security groups
  ([07 §7.2](07-security.md)).
- **The browser only ever talks to the CP.** The CP never touches tmux or a working copy
  itself — always through the agent. The bare repositories of the internal git
  provider are the CP's own ([91](91-internal-git.md)).
- The home directory survives stop, start and image updates. Where it lives depends on
  the target: a bind-mounted directory (`<WS_DATA>/…/<key>/home` on `docker`), a host
  directory (`native`), an EFS access point (`ecs`) or a per-user EBS volume
  (`ecs-ec2`).
- An egress forward proxy can run alongside the CP as a subcommand (`AF_EGRESS_LISTEN`,
  default `:3128`; [07 §7.8](07-security.md)).
- **Engines are not part of the workspace.** On AWS the CP starts them on demand:
  VOICEVOX as an ECS service ([decisions/0070](../decisions/0070-tts-ondemand-engine.md)),
  and on `ecs-ec2` llama.cpp and ComfyUI on GPU instances it buys itself
  ([decisions/0071](../decisions/0071-self-hosted-inference-engines.md),
  [0077](../decisions/0077-engine-boxes-bought-by-cp.md)). On `docker` and `native`, an
  engine already running elsewhere on the network is named by URL
  ([decisions/0076](../decisions/0076-external-image-engine-on-lan.md)). Either way a
  workspace reaches it only through the CP. While a cold engine starts, the gateway
  holds a streaming request open with a heartbeat; a non-streaming one is held for at
  most 45 seconds by default (75 for a borrowed engine; `AF_ENGINE_PLAIN_HOLD`) and
  then answered `503 engine_waking` with `Retry-After`, and the caller retries while the
  engine keeps coming up. Speech is read by Polly wherever Polly is configured — always
  for English, and for Japanese while VOICEVOX is starting or switched off.

## 1.4 Authentication is two layers — do not conflate them

| Layer | Answers | How | Stored |
|---|---|---|---|
| **L1, Console** | who may use the Console at all | `AUTH=oauth` (the CP's own login with Google, GitHub or any OIDC provider; what the compose and AWS templates set) / `proxy` (trust an upstream gateway's email header) / `dev` (a fixed identity; the default when `AUTH` is unset, and the only mode `native` accepts) | a signed session cookie held by the CP |
| **L2, agent** | as whom each user's agent runs | each person's own sign-in with the provider | inside the workspace: the CLI's own config, or `secrets.enc` |

L2 is the user's own business; the Console's job is to **show its state and offer the
connection flow**. Details: L1 in [07 §7.3](07-security.md), L2 in
[08](08-integrations.md).

## 1.5 The main flows

### Login (L1, `AUTH=oauth`)

```
Browser → CP /login → /oauth2/login → the provider → /oauth2/callback
  → admit or refuse, fail-closed: the provider's own gate (GitHub: membership of an
    allowed organisation), then the allowlists (email / domain), which a tenant's
    roster or auto-join domain can also satisfy. How they combine differs per
    provider — GitHub with no email list anywhere admits every member of an allowed
    organisation — and is 07 §7.3.1
  → issue a signed cookie → Console
every request after that: authGate verifies the cookie → sets the email header
  (X-Forwarded-Email by default) → resolveIdentity → the tenant named by X-AF-Tenant
  (the `tenant` query parameter where a browser cannot set headers) is checked against
  a membership, the tenant's allowed providers and its allowed source addresses
  → the handler
```

The full rules are [07 §7.3](07-security.md).

### Starting or attaching to a workspace

```
Console "Start" → CP POST /api/workspace/start
  stopped → Runtime.Start, which returns before the agent is necessarily reachable
            docker: remove any stopped remnant, run the current image, and inject the
                    unwrapped DEK as AF_SECRET_KEY
            ecs / ecs-ec2: bring the member's ECS service up (on ecs-ec2, on a pool
                    slot with the member's EBS home attached); the state reads
                    `starting` until it converges, and the Console keeps polling
  running → nothing to do
→ the response carries the live state
```

A request that must reach the agent next — creating, forking or resuming a session, a
carried answer — starts a stopped workspace itself (`AF_AUTOSTART`, on by default) and
waits for the agent, 55 seconds by default (`AF_AGENT_READY_WAIT_SEC`). Past that it
answers `409 workspace_starting`, and the boot carries on. An override has to stay below
the ingress's idle timeout (60 seconds on the AWS load balancer), or the caller gets a
504 instead of the 409. Connection tracking keeps a workspace warm; the reaper stops it
once it has been idle (two hours by default, set per tenant).

### Creating a session

```
Console: new session (kind, repo/dir, model, execution method, a new worktree by default)
  → CP /api/sessions: start the workspace and wait for the agent as above, then the
    session quota (it counts the agent's live sessions, so it needs the agent up)
    → agent /sessions
  → agent: persist the metadata and start it per driver
      managed: open or resume the conversation on the kind's runtime
      tui: start the CLI inside a tmux session, resuming if there is history
  → Console: managed is driven by the conversation API; tui by the conversation API
    or the terminal WebSocket
```

### Attaching a terminal

```
Browser xterm.js ──WSS /ws/terminal?session=&tenant=──▶ CP
  → check the workspace is running (stopped/starting is a 409; it does not auto-start)
  → dial the agent's /ws/pty with the bearer token → relay both ways
    (binary = PTY output, text = input and resize)
Disconnecting does not kill tmux. Reconnecting returns to the same screen, and several
tabs may attach at once.
```

Only `driver=tui` sessions use that path. A `driver=managed` session has no pane at
all: the Console drives it through `POST /sessions/{name}/turn`, `/respond` and
`/settings` and the transcript API. **Stop, resume, archive and fork have
driver-independent semantics** — the agent dispatches them to tmux or to a runtime
handle as appropriate.

### Cloning a repository

```
Console: Repos → a URL → CP /api/repos → agent: git clone
  (credentials applied transparently by one credential helper; GIT_TERMINAL_PROMPT=0
   so it fails fast instead of hanging)
  → return the parsed status (git status --porcelain=v2) for display
```

## 1.6 Ports and adapters — where the platform dependency is confined

The core — Console, CP logic, agent, workspace image — is identical on every target.
Only interface seams inside the CP change. The mapping and how to choose is
[09](09-deploy.md); what each target can and cannot do is
[ref/deploy-targets](../../guide/ref/deploy-targets.md).

| Port | Interface | one host (`docker`, the default; `native`) | AWS (`ecs`, `ecs-ec2`) |
|---|---|---|---|
| running workspaces | `RuntimeFactory`, chosen by `AF_RUNTIME` | Docker Engine / sandboxed host processes | an ECS task on Fargate / on an EC2 slot from a pool |
| the persistent home | inside the Runtime | a bind-mounted directory / a host directory | an EFS access point / a per-user EBS volume |
| L1 authentication | the `AUTH` switch | `oauth`, `proxy` or `dev` / `dev` only | `oauth` (the template also accepts `dev`) |
| metadata | `Store` | SQLite (default, pure Go) | Postgres (RDS) |
| at-rest keys | `KeyCustodian` | a local custodian derived from the master key | the same; a KMS custodian is only a seam ([decisions/0005](../decisions/0005-envelope-custodian.md), #969) |
| ingress / TLS | outside the CP | Caddy / Funnel | ALB + ACM |
| engines | the engine table | an engine already running on the network, by URL | started on demand by the CP (the GPU ones on `ecs-ec2` only) |

## 1.7 What is built, and what is not

What each screen and agent offers is [ref/features](../../guide/ref/features.md) and
[ref/agents](../../guide/ref/agents.md); this table is the state of the architecture.

| Area | State |
|---|---|
| the deployment targets (`docker`, `native`, `ecs`, `ecs-ec2`, and compose on one VM) | ✅ — the production deployment runs `ecs-ec2` ([09](09-deploy.md)) |
| multi-tenancy (many-to-many identity ↔ tenant, quotas, audit, showback) | ✅ |
| the internal git provider (bare + smart HTTP + LFS) | ✅ ([91](91-internal-git.md)) |
| MCP (the CP endpoint plus the in-container stdio servers) | ✅ — the dangerous admin tools are not planned ([decisions/0006](../decisions/0006-mcp-unified.md)) |
| the agent kinds: claude, codex, cursor, opencode, agy, copilot, kiro, lcpp, muse | ✅ — how one is integrated is [04 §4.3](04-agent.md) |
| self-hosted engines | ✅ — started on demand on AWS (the GPU ones on `ecs-ec2` only); on `docker` / `native`, an engine already running on the network (§1.3) |
| the in-container browser pane | ✅ ([decisions/0018](../decisions/0018-container-browser-pane.md)) |
| egress control | ◐ observation, a versioned allowlist with human approval, and an enforce switch on the proxy; workspace traffic is not yet forced through the proxy ([07 §7.8](07-security.md)) |
| a KMS custodian | 📋 seam only (#969) |
| the Go internal refactor | ✅ done ([decisions/0012](../decisions/0012-go-internal-refactor.md), [0067](../decisions/0067-parallel-refactor.md)); the current layout is [90](90-code-map.md) |
