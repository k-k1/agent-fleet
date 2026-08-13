# Agent Fleet — a self-hosted console for running AI coding agents as a fleet

![The Agent Fleet Console: repo tree with live sessions, a chat mirror of a running agent, and the repository's commit graph side by side](docs/img/console-en.webp)

Agent Fleet is a web service that lets a team share AI coding agents — Claude Code,
Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI, Kiro, OpenCode — efficiently and safely.
Each member gets an isolated per-user environment — a Docker container with cgroup
CPU/memory quotas (or a bubblewrap sandbox in the Docker-less native runtime) — with
a persistent home and git working copies, and starts, drives and manages agent
sessions from the browser. A Go control plane orchestrates the workspaces; the same
core runs **both locally (Docker) and on AWS ECS (CloudFormation)** — the deployment
layer is separated via ports & adapters ([portability](docs/dev/09-deploy.md)).

**Status: Phase 2 complete, Phase 3 in progress.** Multiple users can work in
parallel, mutually invisible, on a single on-prem host (per-user Workspace /
AuthGateway / network isolation / at-rest encryption). Phase 3 productization has
reached the packaging & distribution milestone (P3-10): the full Console rebuild
(React+Vite), the AWS ECS adapter (P3-7) and the compose / ECS / Docker-less native
distribution targets are shipped, with 0.x releases published to the
[distribution repo](https://github.com/k-k1/agent-fleet-dist)
([docs/history/p3-10-packaging.md](docs/history/p3-10-packaging.md),
[docs/roadmap.md](docs/roadmap.md)).
**Current operational details and pitfalls: [docs/HANDOFF.md](docs/HANDOFF.md) (read
first in a new session).**
Code: [`workspace/`](workspace/) (Agent + image) / [`control-plane/`](control-plane/) /
[`console/`](console/); start via [`deploy/local/run-dev.sh`](deploy/local/run-dev.sh)
(subcommands: `local` = Docker default / `wsl` = WSL preset / `native` = no Docker /
`reset` = wipe data. [docs/dev/10 §10.3](docs/dev/10-development.md)).

## Self-hosting (on-prem / Docker Compose)

Each company runs one deployment on its own infrastructure. Just `compose up` the
image set (Caddy handles automatic TLS via Let's Encrypt; login uses the CP-native
Google OAuth).

```bash
cd deploy/compose
cp .env.example .env     # generate and fill in secrets (AF_MASTER_KEY etc.)
docker build -t agent-fleet/workspace:dev ../../workspace   # per-user workspace image
docker compose up -d --build
```

The snippet above builds the images from this tree, which is what you want while
developing. A **release** is consumed the other way round: the bundle from the
[distribution repo](https://github.com/k-k1/agent-fleet-dist) pulls pinned images from
GHCR ([decisions/0037](docs/decisions/0037-registry-policy.md)).

Procedures, key generation, backup/restore, upgrades, incident response and DooD
constraints are collected in **[deploy/compose/README.md](deploy/compose/README.md)
(runbook)**. Local dev (host processes) remains
[`deploy/local/run-dev.sh`](deploy/local/run-dev.sh); personal WSL use (with or
without Docker) is covered by
[deploy/local/README-wsl.md](deploy/local/README-wsl.md).

## A look around

| | |
|---|---|
| ![Launch dialog: pick the agent CLI, its model, reasoning effort, start mode and whether to run in a fresh git worktree](docs/img/launch-en.webp) | ![Chat mirror: the agent's question rendered as an answerable card with the options it offered](docs/img/mirror-en.webp) |
| **Start anything from one dialog** — agent, model, reasoning effort, start mode, and a fresh git worktree or the working copy as-is. | **Follow and steer from the browser** — questions, plans and permission prompts arrive as cards you answer in place. |
| ![Three panes: the chat mirror, a live terminal attached to a shell session, and the repository's working-tree changes with a commit box](docs/img/split-en.webp) | ![Split panes: the commit graph with branch lanes on the left, the selected commit's diff on the right](docs/img/scm-en.webp) |
| **Split panes** — mirror, live terminal and working-tree changes side by side; each pane can also pop out into its own tab. | **Real git, in the console** — commit graph beside the selected commit's diff, plus staging and commit, per working copy and worktree. |
| ![Usage tab: a stacked per-feature token chart over 30 days, KPI tiles for tokens, calls, cache reads, API-equivalent cost and unmeasured calls, and breakdowns by feature, agent and model](docs/img/usage-en.webp) | ![A terminal pane attached to a shell session, showing a build and a git status run](docs/img/terminal-en.webp) |
| **See where the tokens went** — per feature, per agent and per model, over 24h / 7d / 30d. Calls that report no tokens are counted separately, never as zero. | **A real terminal, too** — every session (agent or plain shell) is attachable as a live PTY. |

The UI is English or Japanese, switched per user in ⚙ Settings — every view above also
exists in Japanese (`docs/img/*-ja.webp`, e.g.
[the console](docs/img/console-ja.webp)). Screenshots are captured from the real
Console bundle against a demo dataset — regenerate them with
`node console/scripts/shots/capture.mjs --locale en` (the default locale is `ja`;
[how](console/scripts/shots/README.md)).

## Settled assumptions (v1)

| Topic | Decision | Rationale / notes |
|------|------|-----------|
| Agent auth | each user connects their own account/seat from the Console (Claude: OAuth code paste; Codex: ChatGPT device code or API key; Copilot rides the GitHub connection; Cursor / Kiro: browser sign-in; OpenCode: provider API keys or an opencode account) | the console surfaces each user's auth state and prompts re-login; a manual `/login` in the terminal still works as a fallback |
| User isolation | one container per user | highly portable, strong isolation, fits AWS well |
| Target scale | ~20 users (concurrent) | a single cluster + an orchestration layer is enough |
| Persistence | `local`=bind mount / `aws`=EBS/EFS | home, clones, credentials and history are kept on disk |
| Git auth | HTTPS tokens/OAuth via Console (Connections) | downgraded from SSH keys; the CP holds no secrets ([decisions/0003](docs/decisions/0003-ssh-to-connections.md)) |
| Tech stack | Console=React+Vite / Backend=Go | Go suits daemons, WS proxying and container control ([decisions/0004](docs/decisions/0004-vanilla-to-react.md)) |
| Delivery model | packaged product, self-hosted per company | 1 company = 1 deployment. SaaS abandoned due to ToS ([decisions/0001](docs/decisions/0001-self-host-vs-saas.md)) |
| Deployment layer | local / aws switchable over one core | separated via ports & adapters (local = Docker, local-first) |

## Documentation layout

Index: [docs/README.md](docs/README.md). **Source of truth for specs =
[docs/dev/](docs/dev/README.md) (for developers) plus the code; for operations =
[docs/guide/](docs/guide/README.md) (for users); for runtime state =
[HANDOFF](docs/HANDOFF.md).**
Decisions (why) = `decisions/`; forward-looking plans = `roadmap.md`; finished plans
and completed feature designs = `history/`.

**Developer docs [docs/dev/](docs/dev/README.md)** (designs and contracts that track
the code)
| File | Contents |
|----------|------|
| [01-architecture](docs/dev/01-architecture.md) | delivery model, terminology, 3-process layout, 2-layer auth, main flows, adapters |
| [02-console](docs/dev/02-console.md) / [03-control-plane](docs/dev/03-control-plane.md) / [04-workspace-agent](docs/dev/04-workspace-agent.md) | per-component design |
| [05-api-contracts](docs/dev/05-api-contracts.md) / [06-data-model](docs/dev/06-data-model.md) | API boundaries and relaying / data model |
| [07-security](docs/dev/07-security.md) / [08-integrations](docs/dev/08-integrations.md) | threat model, auth, crypto / external integrations |
| [09-deploy](docs/dev/09-deploy.md) / [10-development](docs/dev/10-development.md) | deployment & portability / development practices |
| [90-code-map](docs/dev/90-code-map.md) / [91-internal-git](docs/dev/91-internal-git.md) | code map / internal git provider |
| [92-tui-modal-driving](docs/dev/92-tui-modal-driving.md) / [93-worktree-dependencies](docs/dev/93-worktree-dependencies.md) | driving a CLI's modal TUI / what a worktree shares vs. duplicates per ecosystem |

**User guide [docs/guide/](docs/guide/README.md)**: split by persona (member / admin /
operator / lite).

**Handoff & plans**
| File | Contents |
|----------|------|
| [docs/HANDOFF.md](docs/HANDOFF.md) | this host's runtime state, working practices, pitfalls, current position |
| [docs/CHANGELOG-handoff.md](docs/CHANGELOG-handoff.md) | chronological log (date + one line) |
| [docs/roadmap.md](docs/roadmap.md) | phase list, milestones + Phase 3 detailed design (P3-1–P3-10) |

> The old `docs/reference/` was reorganized into dev/ (mapping table in
> [docs/README.md](docs/README.md)).

**decisions/ — decision records (why, and the discarded options)** — the table below
is an excerpt; the full set (0001–0042) is in [docs/decisions/](docs/decisions/)
| File | Contents |
|----------|------|
| [0001-self-host-vs-saas.md](docs/decisions/0001-self-host-vs-saas.md) | delivery model: SaaS abandoned, per-company self-hosting adopted (ToS grounds, residual risk) |
| [0002-claude-auth-onboarding.md](docs/decisions/0002-claude-auth-onboarding.md) | Claude auth: auth and onboarding are distinct (root cause of the login screen) |
| [0003-ssh-to-connections.md](docs/decisions/0003-ssh-to-connections.md) | git auth: SSH keys → Connections (HTTPS tokens/OAuth) |
| [0004-vanilla-to-react.md](docs/decisions/0004-vanilla-to-react.md) | Console stack: React + Vite adopted |
| [0005-envelope-custodian.md](docs/decisions/0005-envelope-custodian.md) | at-rest keys: envelope encryption + custodian abstraction (on-prem limits stated) |

**history/ — finished implementation plans (done, kept for the record)** — the table
below is an excerpt; the full set is in [docs/history/](docs/history/)
| File | Contents |
|----------|------|
| [phase0-poc.md](docs/history/phase0-poc.md) | Phase 0 PoC procedure (`/login` verification) |
| [phase1-plan.md](docs/history/phase1-plan.md) | Phase 1 plan + results (§11.10 remains useful knowledge) |
| [p3-1-metadatastore.md](docs/history/p3-1-metadatastore.md) | P3-1: MetadataStore (SQLite) |
| [p3-2-identity-tenant.md](docs/history/p3-2-identity-tenant.md) | P3-2: identity↔tenant many-to-many |
| [p3-3-envelope-crypto.md](docs/history/p3-3-envelope-crypto.md) | P3-3: envelope encryption + custodian abstraction |
| [p3-4-quota.md](docs/history/p3-4-quota.md) | P3-4: resource budgets / quotas |
| [p3-5-member-console.md](docs/history/p3-5-member-console.md) | P3-5: member Console UX (git/file visibility) |
| [p3-10-packaging.md](docs/history/p3-10-packaging.md) | P3-10: packaging & distribution (compose / ECS / native targets, release bundles) |
| [console-redesign.md](docs/history/console-redesign.md) | Console UI rebuild brief (vanilla→React diagnosis) |

## Existing prototype assets (reused from)

A personal fleet-operation setup already existed; this project turns it into a
product.

- **`oauth2-proxy`** — Google domain-restricted auth gate (`emails.txt` allowlist).
  **Now replaced by CP-native Google OAuth (`AUTH=oauth`)** — the allowlist is
  `deploy/local/allowed-emails.txt` (emails / `@domain`). Design:
  [docs/dev/07 §7.3](docs/dev/07-security.md)
- **`scripts/tmux-claude.sh`** — idempotently starts, resumes and generation-manages
  multiple Claude CLIs in detached tmux
- **`CLAUDE_CONFIG_DIR` profile separation** — per-directory separate `~/.claude`
- **`~/.claude/settings.json`** — `remoteControlAtStartup` /
  `skipDangerousModePermissionPrompt` preconfigured

## Terminology

- **Workspace** — the persistent container environment for one user, with a home
  volume and running processes.
- **Working copy** — the working directory of a git repository cloned inside a
  Workspace.
- **Session** — the logical unit of a conversation, its settings and execution state,
  tied to a working copy. It does not imply a terminal: Codex / OpenCode / Copilot /
  Cursor / Kiro default to a **managed** execution method driven from the chat view
  (Codex and OpenCode run on a shared runtime with no per-session CLI process at all),
  while Claude / Antigravity and the plain shell / SSM sessions use a terminal.

## License

[Apache License 2.0](LICENSE) (permissive, with a patent grant). Publishing the
source of a credential-handling tool so each company can audit the crypto/isolation
implementation is part of the adoption pitch. Contributions:
[CONTRIBUTING.md](CONTRIBUTING.md); vulnerability reports and the threat model:
[SECURITY.md](SECURITY.md).
