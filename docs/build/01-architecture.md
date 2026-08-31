---
audience: "everyone's first chapter — anyone who wants the shape of the whole thing"
source_of_truth: "the code (this is a map and a statement of intent)"
updated: "2026-07"
---

# 01. Architecture

English | [日本語](01-architecture.ja.md)

## 1.1 What it is, and how it is delivered

A self-hosted web service that lets several members of one organisation share CLI
coding agents. Each user gets an isolated container — a **workspace** — where git
repositories live, and drives sessions, terminals, git, files and chat from a browser
Console.

- **Delivery model**: a packaged product, self-hosted by each company. **One company =
  one deployment.** SaaS was abandoned on terms-of-service grounds
  ([decisions/0001](../decisions/0001-self-host-vs-saas.md)).
- **Scale assumed**: about 20 concurrent people, several sessions each. One host — or
  one cluster — is enough.
- **Agent credentials are brought by the user.** Each person signs in with their own
  account ([08 §8.5](08-integrations.md)).
- **The deployment target is the company's choice**: on-premise Docker by default, or
  their own AWS ([09](09-deploy.md)). One core, with only the deployment layer swapped
  through ports and adapters (§1.6).

## 1.2 Terms

| Term | Definition |
|---|---|
| Workspace | The persistent container environment for one membership (identity × tenant). Holds the home directory, the encrypted store and the working copies |
| Working copy | The directory of a git repository cloned inside a workspace (`~/repos/<name>`) |
| Session | The logical unit of a conversation, its settings and its execution state, tied to a working copy or an arbitrary directory. It has a kind and a driver, and is **not** necessarily one-to-one with a process or a tmux pane |
| Driver | How a session is controlled. `managed` means a shared runtime plus a structured API; `tui` means a CLI screen inside tmux. The user-facing names are "execution method", "Managed" and "Terminal (CLI)" |
| Control Plane (CP) | The resident backend outside the workspaces: authentication, orchestration, relaying, persistence |
| Workspace Agent | The resident process inside each workspace container. **The only thing that touches tmux, git, the filesystem and the CLI agents directly** |
| Console | The browser SPA (React + Vite), served statically by the CP |
| Tenant / Identity / Membership | A department / a person / the many-to-many join between them. Workspaces are separated per membership ([06](06-data.md)) |

## 1.3 Three processes (Docker is the default)

```
Browser (Console SPA: React + Vite + zustand, xterm.js + browser-pane canvas)
   │ HTTPS / WSS
   ▼
[edge]  Caddy (automatic TLS, compose) / Tailscale Funnel / ALB … operator's choice (09 §9.3)
   │ passes through to 127.0.0.1 loopback
   ▼
Control Plane (resident Go process; CP_ADDR defaults to :8080, compose 127.0.0.1:8099)
   │  · authGate (L1) → resolve identity/membership → authorise
   │  · serves the Console bundle / REST / WS / SSE
   │  · workspace lifecycle (Runtime adapter: docker | ecs)
   │  · metadata store (SQLite by default | Postgres)
   │  · internal git provider / MCP / audit / egress / memos / reaper
   │
   │  relays: REST / SSE / terminal WS / browser REST+WS / preview
   │  auth: a per-container bearer token, injected by the CP at start
   ▼
Workspace Agent (Go, inside the per-user container; AGENT_ADDR defaults to :7700)
   │  · session lifecycle / driver selection and recovery
   │  · the managed runtime, shared per workspace
   │  · tmux / PTY for the terminal-driven kinds
   │  · git / filesystem / connections (the encrypted store `secrets.enc`)
   │  · chat (headless CLI) / transcript / usage
   │  · the browser manager (Chromium over CDP, pages, JPEG screencast, input)
   │  · a preview relay to services running inside the container
   ▼
a shared runtime or a CLI agent, plus the git working copies (~/repos)
```

- Containers are named per workspace and joined to a dedicated network, so they cannot
  reach each other. The agent's port is published on the host's loopback only, so the
  CP is the only thing that can reach it ([07 §7.2](07-security.md)).
- **The browser only ever talks to the CP.** The CP never touches tmux or git itself —
  always through the agent.
- The home directory is a bind mount and survives everything. Updating the image does
  not touch it.
- An egress forward proxy can run alongside the CP as a subcommand
  ([07 §7.8](07-security.md)).

## 1.4 Authentication is two layers — do not conflate them

| Layer | Answers | How | Stored |
|---|---|---|---|
| **L1, Console** | who may use the Console at all | `AUTH=oauth` (the CP's own OAuth, the default) / `proxy` (trust an upstream gateway's headers) / `dev` (a fixed identity) | a signed session cookie held by the CP |
| **L2, agent** | as whom each user's agent runs | each person's own sign-in with the provider | inside the workspace: the CLI's own config, or `secrets.enc` |

L2 is the user's own business; the Console's job is to **show its state and offer the
connection flow**. Details: L1 in [07 §7.3](07-security.md), L2 in
[08](08-integrations.md).

## 1.5 The main flows

### Login (L1, `AUTH=oauth`)

```
Browser → CP /login → /oauth2/login → the provider → /oauth2/callback
  → check the allowlist (email / domain, fail-closed) → issue a signed cookie → Console
every request after that: authGate verifies the cookie → injects the identity
  → validates the tenant header against a membership → the handler
```

### Starting or attaching to a workspace

```
Console "Start" → CP checks workspace.state
  stopped → Runtime.Start (remove, then run with a fresh image; unwrap the DEK and
            inject the secret key) → wait for running
  running → nothing to do
→ the CP can now relay to the agent. Connection tracking keeps it warm; the reaper
  stops it when idle.
```

### Creating a session

```
Console: new session (kind, repo/dir, model, worktree by default)
  → CP /api/sessions (quota check, DB mirror) → agent /sessions
  → agent: persist the metadata and start it per driver
      managed: create or resume a thread on the shared runtime
      tui: start the CLI inside a tmux session, resuming if there is history
  → Console: managed is driven by the conversation API; tui by the conversation API
    or the terminal WebSocket
```

### Attaching a terminal

```
Browser xterm.js ──WSS /ws/terminal──▶ CP
  → check the workspace is running (stopped/starting is a 409; it does not auto-start)
  → dial the agent's PTY endpoint with the bearer token → relay both ways
    (binary = PTY output, text = input and resize)
Disconnecting does not kill tmux. Reconnecting returns to the same screen, and several
tabs may attach at once.
```

Only `driver=tui` sessions use that path. A `driver=managed` session has no pane at
all: the Console drives it through the turn / respond / settings endpoints and the
transcript API. **Stop, resume, archive and fork have driver-independent semantics** —
the agent dispatches them to tmux or to a runtime handle as appropriate.

### Cloning a repository

```
Console: Repos → a URL → CP /api/repos → agent: git clone
  (credentials applied transparently by one credential helper; terminal prompting is
   disabled so it fails fast instead of hanging)
  → return the parsed status for display
```

## 1.6 Ports and adapters — where the platform dependency is confined

The core — Console, CP logic, agent, workspace image — is identical on every target.
Only interface seams inside the CP change. The mapping and how to choose is
[09](09-deploy.md).

| Port | Interface | local (default) | aws |
|---|---|---|---|
| running containers | `Runtime` / `RuntimeFactory` | Docker Engine | ECS |
| the persistent home | inside the Runtime | bind mount | an EFS access point |
| L1 authentication | the `AUTH` switch | oauth / dev | proxy (ALB OIDC) |
| metadata | `Store` | SQLite (default, pure Go) | Postgres |
| at-rest keys | `KeyCustodian` | a local custodian derived from the master key | KMS / Vault — seam only ([decisions/0005](../decisions/0005-envelope-custodian.md)) |
| ingress / TLS | outside the CP | Caddy / Funnel | ALB + ACM |

## 1.7 What is built, and what is only designed

| Area | State |
|---|---|
| local / Docker deployment | ✅ in use |
| multi-tenancy (many-to-many identity ↔ tenant, quotas, audit, showback) | ✅ |
| the internal git provider (bare + smart HTTP + LFS) | ✅ ([91](91-internal-git.md)) |
| MCP (the CP endpoint plus in-container stdio) | ✅ — the dangerous admin tools remain |
| egress control | 🚧 log-only and allowlist; enforce is still to come |
| the AWS adapter (ECS / EFS / SSM, CloudFormation) | 🚧 implemented, no production mileage ([09](09-deploy.md)) |
| KMS / Vault custodian | 📋 seam only |
| the `agy` kind | ✅ ([decisions/0008](../decisions/0008-antigravity-cli-agent-kind.md)) |
| the `copilot` kind, Terminal + Managed | ✅ ([decisions/0019](../decisions/0019-copilot-agent-kind.md)) |
| the `kiro` kind, Terminal + Managed | ✅ ([decisions/0026](../decisions/0026-kiro-agent-kind.md)) |
| the in-container browser pane | ✅ ([decisions/0018](../decisions/0018-container-browser-pane.md)) |
| the Go internal refactor | mostly landed; what remains is in [decisions/0012](../decisions/0012-go-internal-refactor.md), and the current layout is [90](90-code-map.md) |
