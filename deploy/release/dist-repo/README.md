# agent-fleet-dist

English | [日本語](README.ja.md)

Distribution artifacts for [Agent Fleet](https://github.com/k-k1/agent-fleet).
**There is no source code here** — binaries and bundles are attached to Releases.

## What is Agent Fleet?

Agent Fleet is a self-hosted web console for running AI coding agents
(Claude Code, Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI,
OpenCode) as a managed fleet. Each member gets an isolated workspace — a
Docker container with cgroup CPU/memory quotas (or a bubblewrap-sandboxed
rootfs in the native edition) with a persistent home and git working copies —
and starts, drives and monitors agent sessions from the browser. A Go control plane orchestrates the
workspaces.

Key features:

- **Six agent CLIs, one console** — run Claude Code / Codex / GitHub Copilot /
  Antigravity / Cursor / OpenCode sessions side by side, with per-session model
  choice. CLI versions are pinned to verified combinations (opt-in self-update).
- **Parallel sessions on real git repos** — clone over HTTPS (GitHub /
  Bitbucket tokens or OAuth device flow) with **Git LFS, submodules (incl.
  nested) and git-worktree support**; run multiple sessions per repo isolated
  in worktrees, follow each conversation live in a mirror view with terminal
  access, queue input while the agent works, and open plain **shell sessions**
  next to agent sessions. **Subversion works too** — check out over URL + basic
  auth (subtree and multiple-path checkouts, optional per-server trust for
  self-signed certificates, automatic working-copy lock recovery).
- **Project-centric console** — file browser, commit graph and diffs, session
  state badges (working / awaiting input), a memo queue with image attachments,
  a notification center, English/Japanese UI, keyboard-first operation
  (command palette / leader key), and optional text-to-speech for replies
  (VOICEVOX / Zundamon, AWS Polly).
- **Live app preview** — web apps started inside a workspace (Vite HMR,
  WebSocket, Spring Boot, …) render in an embedded browser pane; ports are
  reachable through lightweight previews.
- **Multi-user by design** — Google OAuth login, tenants and roles
  (member / admin / operator), per-user network isolation, envelope encryption
  for secrets at rest, and per-workspace memory quotas.
- **Usage visibility** — see each agent account's usage and rate limits (and
  when they reset) at a glance, plus per-session context usage with warnings
  and summarized handover before the context window fills up.
- **Assistant chat & fleet orchestration** — a built-in assistant that can
  drive the fleet: start and steer multiple agent sessions, orchestrate work
  across different agents and hand tasks over between them with summarized
  context, and act as an **SRE assistant** through PagerDuty / Grafana /
  CloudWatch integrations and AWS SSM login sessions to your servers.
- **Scheduled execution** — have the assistant schedule recurring agent runs in
  plain language ("every weekday at 9:00, review yesterday's changes"): the
  control plane fires them on a wall-clock (cron / interval / one-off, timezone-
  and DST-aware), **waking a stopped workspace**, running the prompt, and
  reporting back — so timed work happens even while nobody is watching. Reuse a
  long-lived session to build up context across runs, or start fresh each time;
  browse the schedule list and per-run history (with the session each run drove)
  from the left rail.
- **Chat bridge (Discord / Slack)** — connect a bot from the Console (guided,
  token-paste wizard) and each session gets its own thread: replies ready,
  questions, plan approvals, permission requests, abnormal exits and completion
  reports arrive there. Reply in the thread to steer the session, answer
  questions and approve plans with buttons, or @mention the fleet operator to
  drive the whole fleet from chat — destructive actions triggered from chat
  stop at an approve/deny gate first. An opt-in full-text mode posts the
  agent's actual replies (with automatic secret redaction).
- **Operable** — backup/restore scripts, forward-only DB migrations for
  upgrades, air-gap installation paths, and MCP integration points.

Each user signs in to the agent CLIs with **their own account/seat** (e.g. a
Claude subscription) from the console; the deployment itself does not bundle or
share any AI-provider credentials.

## Which agent does what

Not every capability is available on every agent CLI — some are gated by what the
upstream CLI exposes. This matrix is the quick reference (✓ = supported,
— = not applicable / not supported):

| Capability | Claude | Codex | Cursor | Copilot | Antigravity | OpenCode | Shell |
|---|:--:|:--:|:--:|:--:|:--:|:--:|:--:|
| Managed (paneless) execution | — | ✓ | ✓ | ✓ | — | ✓ | — |
| Terminal (CLI) execution | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Live chat mirror | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| History when stopped (read-only) | ✓ | ✓ | —³ | ✓ | ✓ | ✓ | — |
| Model choice at launch | ✓ | ✓ | ✓ | ✓¹ | ✓ | ✓ | — |
| Reasoning-effort control | ✓ | ✓ | —² | ✓ | —² | ✓ | — |
| Plan mode | ✓ | ✓ | ✓ | ✓ | — | ✓ | — |
| Context-window gauge | ✓ | ✓ | — | — | — | ✓ | — |
| Image paste | ✓ | ✓ | — | — | ✓ | ✓ | — |
| Hand off a conversation | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Runs in a git worktree | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| Scheduled (unattended) runs | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Chat bridge (Discord / Slack) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | — |
| Usable as the assistant chat | ✓ | ✓ | ✓ | — | ✓ | ✓ | — |
| WS-bar usage / limit chip | ✓ | ✓ | — | ✓ | ✓ | — | — |

¹ Copilot's model choice is plan-dependent (Free = Auto only).
² Cursor and Antigravity fold the reasoning effort into the model name, so there is
no separate control. The WS-bar usage chip needs an account-level limit to show —
opencode (bring-your-own provider API keys) and Cursor expose none. **SSM** sessions
(remote login over AWS SSM) behave like Shell: terminal only, no conversation, and not
tied to a workspace worktree.

³ Cursor's managed (default) execution keeps no local transcript — a **stopped** Cursor
session has no history to show (the live mirror works while running, and running Cursor
as Terminal (CLI) does persist a readable history).

**Default model for the assistant chat** — each assistant can pin its own, and Claude's
default is also settable deployment-wide via `AF_CHAT_MODEL`. These favour fast, low-cost
tiers because the assistant is conversational: Claude → Sonnet 5 · Codex → `gpt-5.6-luna`
· OpenCode → `opencode/nemotron-3-ultra-free` · Antigravity → Gemini 3.5 Flash · Cursor →
its own default (Auto). Cursor's assistant runs **read-only** (`--mode ask`).

## Getting started — which edition?

| Your situation | Edition | What you need |
|---|---|---|
| Personal use on WSL2 or a single-user Linux machine; no Docker | **Native** (below) | x86_64 Linux/WSL2 with unprivileged user namespaces (stock WSL2 works), `curl` or `wget`, ~1.5 GB disk |
| A team on your own Linux server | **Docker Compose** (below) | Docker Engine + `docker compose`, a public domain pointed at the host (auto-TLS; an internal-CA fallback exists), a Google OAuth 2.0 client for login |

Common to all editions: outbound network is needed once per workspace to
pin-install the agent CLIs on first start (air-gap alternatives are documented
in the bundled READMEs), and each user needs their own agent account/seat.

## Installing the native edition (no Docker; WSL2 / single-user Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start
# then open http://localhost:8099 in your browser
```

- The installer downloads the latest release tar, verifies it against
  `SHA256SUMS`, extracts it to `~/.local/opt/agent-fleet/<version>/` and
  symlinks `~/.local/bin/af`.
- Updating uses the same command (your data lives in
  `~/.local/share/agent-fleet` and is never touched). You can also update in
  place with `af update`.
- **Automatic updates:** the installer enables a daily systemd user timer that
  runs `af update` to *stage* the latest release (sha256-verified). It never
  restarts a running service — apply it when convenient via
  `systemctl --user restart agent-fleet` or the Console's "restart to apply"
  button, so live agent sessions are never dropped. Opt out with
  `AF_NO_AUTOUPDATE=1 bash install.sh`.
- To pin a version: `AF_VERSION=0.1.0 bash install.sh` (also stops auto-updates
  advancing past it when set in the unit `Environment=`)
- For details (host requirements, air-gap installs, running as a service,
  optional text-to-speech with VOICEVOX / Zundamon, limitations) see the
  `README.md` bundled inside the tar.

### Cloning private repos (git-provider OAuth)

Each user connects their own GitHub / Bitbucket from the Console
(**⚙ Settings → Git**) — pasting a token works out of the box. To also light up
the one-click **"Connect via OAuth"** buttons, set these in the environment
**before `af start`** (the launcher passes them through to the control plane):

| Provider | Variables | Notes |
|---|---|---|
| **GitHub** (device flow) | `GITHUB_OAUTH_CLIENT_ID` | Create an OAuth App with **"Enable Device Flow" ON**. The client_id is **not a secret**; no callback URL is needed, so it works on plain `localhost`. |
| **Bitbucket** (auth code) | `BITBUCKET_OAUTH_KEY`, `BITBUCKET_OAUTH_SECRET`, `PUBLIC_BASE_URL` | The consumer's Callback URL must exactly equal `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback`. |

```bash
GITHUB_OAUTH_CLIENT_ID=<your-client-id> af start
```

Under the systemd unit below, add them as `Environment=` lines in `[Service]`.
Without any of this, token/PAT paste in the Console still works — OAuth is only a
convenience.

Run it as a service instead of foreground `af start` (systemd is on by default in
WSL2) — create `~/.config/systemd/user/agent-fleet.service`:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/.local/bin/af start
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
systemctl --user status agent-fleet          # expect: Active: active (running)
loginctl enable-linger "$USER"               # keep running after you close the WSL session
```

`%h` is your home directory; the `ExecStart` path matches the one-liner install
above (symlinked `~/.local/bin/af`). Stop any foreground `af start` first, or the
service fails to bind port 8099.

## Installing the Docker Compose edition (team, on-prem)

The images are **not** published to a registry, so the bundle
(`agent-fleet-<version>.tar.gz`) and the images tar
(`agent-fleet-images-<version>.tar.gz`) ship on
[Releases](https://github.com/k-k1/agent-fleet-dist/releases). A helper fetches
and verifies both, extracts the bundle and `docker load`s the images:

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install-compose.sh | bash
cd agent-fleet-<version>
cp .env.example .env     # fill in secrets, domain, Google OAuth (see below)
docker compose up -d
```

Unlike the native edition this is **not** a full one-liner: you must edit `.env`
(secrets, `PUBLIC_DOMAIN`, Google OAuth, optionally the git-provider OAuth vars
below) before `docker compose up`. To pin a version, prefix
`AF_VERSION=<version>`; `AF_SKIP_IMAGES=1` skips the large images download when
you hand it off separately. Prefer the manual path? Download the two tars, then
`sha256sum -c --ignore-missing SHA256SUMS`, `tar xzf`, `./load-images.sh`.

The bundled `README.md` is the full runbook: prerequisites, key generation,
TLS/domain setup, git-provider OAuth (`GITHUB_OAUTH_CLIENT_ID` /
`BITBUCKET_OAUTH_KEY` / `BITBUCKET_OAUTH_SECRET` in `.env`), backup/restore,
upgrades and troubleshooting.

## Uninstalling / removing data (native edition)

```bash
# 1. If you set up the systemd user unit, stop and remove it first
systemctl --user disable --now agent-fleet 2>/dev/null
rm -f ~/.config/systemd/user/agent-fleet.service
# otherwise just stop `af start` with Ctrl-C

# 2. Wipe all data — DB, workspace homes (incl. Claude logins), extracted rootfs
af reset --all

# 3. Remove the program itself
rm -f ~/.local/bin/af
rm -rf ~/.local/opt/agent-fleet
```

- Steps 2 and 3 are independent: to uninstall but **keep your data** (e.g. before
  reinstalling), skip step 2 — data lives in `~/.local/share/agent-fleet`
  (or `$WS_DATA` if you overrode it), separate from the program.
- If `af` is already gone, remove the data manually. Note that Go module caches
  inside workspace homes are write-protected, so restore write permission first:

  ```bash
  chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet
  ```

## Release layout

| tag | assets | purpose |
|---|---|---|
| `v<version>` | `agent-fleet-<version>.tar.gz` (compose bundle) / `agent-fleet-images-<version>.tar.gz` (air-gap images) / `agent-fleet-native-<version>-linux-amd64.tar.gz` (native) / `SHA256SUMS` | the application release |
| `rootfs-<r>` | `agent-fleet-rootfs-<r>-linux-amd64.tar.zst` | workspace rootfs the native edition downloads on first start. **Not for standalone use** (`rootfs.json` inside the native tar pins its version and sha256) |

`<r>` is a content hash: when the app version bumps but the rootfs is unchanged,
the same tag is referenced and no re-download happens. Always verify downloads
against `SHA256SUMS` / the sha256 in `rootfs.json` (install.sh and `af start` do
this automatically).

## License / bundled software

- The distributed images and rootfs are a **lean build**: the agent CLIs
  (Claude Code / Codex / GitHub Copilot / Antigravity / Cursor / OpenCode) are
  not bundled. On first start each user fetches verified, pinned versions from
  the respective upstream and signs in with their own account. This distribution
  intentionally does not redistribute the proprietary CLIs.
- For attribution of the bundled OSS, see the `NOTICE` file inside each tar.

## Disclaimer — autonomous agent execution

Agent Fleet runs AI coding agents that act on your behalf: they execute commands,
edit files, and commit and push to remote repositories. That includes running
**unattended** (scheduled runs that wake a stopped workspace), in
**permission-bypassing modes**, and through **shell / SSM sessions that run the
strings you send verbatim**. Such actions can be destructive or irreversible
(deleting data, force-pushing, changing infrastructure) and can incur charges on
your AI-provider and cloud accounts.

You are solely responsible for the workspaces, credentials, repositories, and
infrastructure you connect, and for reviewing what the agents do. Operate with
least-privilege credentials, keep backups, and prefer the approval gates
(shell-command confirmation, chat-bridge approve/deny) for destructive actions.

This software is distributed under the **Apache License 2.0** and, as stated in
that license, is provided **"AS IS", WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND**; the authors and contributors accept **no liability** for any damage, data
loss, downtime, or cost arising from its use (see `LICENSE`, sections 7–8). The
same applies to the third-party agent CLIs and services you connect — their use is
governed by their own terms.
