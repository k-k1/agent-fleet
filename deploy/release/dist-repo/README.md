# agent-fleet-dist

English | [日本語](README.ja.md)

Distribution artifacts for [Agent Fleet](https://github.com/k-k1/agent-fleet).
**There is no source code here** — binaries and bundles are attached to Releases.

## What is Agent Fleet?

Agent Fleet is a self-hosted web console for running AI coding agents
(Claude Code, Codex CLI, GitHub Copilot CLI, Antigravity CLI, OpenCode) as a
managed fleet. Each member gets an isolated workspace — a Docker container with
cgroup CPU/memory quotas (or a bubblewrap-sandboxed rootfs in the native
edition) with a persistent home and git working copies — and starts, drives and
monitors agent sessions from the browser. A Go control plane orchestrates the
workspaces.

Key features:

- **Five agent CLIs, one console** — run Claude Code / Codex / GitHub Copilot /
  Antigravity / OpenCode sessions side by side, with per-session model choice.
  CLI versions are pinned to verified combinations (opt-in self-update).
- **Parallel sessions on real git repos** — clone over HTTPS (GitHub /
  Bitbucket tokens or OAuth device flow) with **Git LFS, submodules (incl.
  nested) and git-worktree support**; run multiple sessions per repo isolated
  in worktrees, follow each conversation live in a mirror view with terminal
  access, queue input while the agent works, and open plain **shell sessions**
  next to agent sessions.
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
- **Operable** — backup/restore scripts, forward-only DB migrations for
  upgrades, air-gap installation paths, and MCP integration points.

Each user signs in to the agent CLIs with **their own account/seat** (e.g. a
Claude subscription) from the console; the deployment itself does not bundle or
share any AI-provider credentials.

## Getting started — which edition?

| Your situation | Edition | What you need |
|---|---|---|
| Personal use on WSL2 or a single-user Linux machine; no Docker | **Native** (below) | x86_64 Linux/WSL2 with unprivileged user namespaces (stock WSL2 works), `curl` or `wget`, ~1.5 GB disk |
| A team on your own Linux server | **Docker Compose** (below) | Docker Engine + `docker compose`, a public domain pointed at the host (auto-TLS; an internal-CA fallback exists), a Google OAuth 2.0 client for login |
| A team on AWS | **ECS (CloudFormation)** (below) | An AWS account, ECR for the images, the templates bundled in the compose tar |
| Offline / restricted network | Any of the above, air-gap paths | The images tar (Compose) or the `-bundle` native tar; file hand-off instead of downloads |

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
  `~/.local/share/agent-fleet` and is never touched).
- To pin a version: `AF_VERSION=0.1.0 bash install.sh`
- For details (host requirements, air-gap installs, running as a service,
  optional text-to-speech with VOICEVOX / Zundamon, limitations) see the
  `README.md` bundled inside the tar.

## Installing the Docker Compose edition (team, on-prem)

The images are **not** published to a registry — download both the bundle
(`agent-fleet-<version>.tar.gz`) and the images tar
(`agent-fleet-images-<version>.tar.gz`) from
[Releases](https://github.com/k-k1/agent-fleet-dist/releases), then:

```bash
V=<version>
sha256sum -c --ignore-missing SHA256SUMS          # verify both downloads
tar xzf "agent-fleet-$V.tar.gz" && cd "agent-fleet-$V"
./load-images.sh "../agent-fleet-images-$V.tar.gz" # docker load (CP + workspace)
cp .env.example .env                               # fill in secrets, domain, Google OAuth
docker compose up -d
```

The bundled `README.md` is the full runbook: prerequisites, key generation,
TLS/domain setup, backup/restore, upgrades and troubleshooting.

## Installing on AWS (ECS / CloudFormation)

The compose bundle also carries the AWS deploy surface under `aws/`:
CloudFormation templates for an ECS deployment (`aws/ecs/cfn/`), a script to
push the released images to your ECR (`aws/ecs/release-ecr.sh`), and a
single-EC2 variant (`aws/ec2-single/`). Start from `aws/ecs/README.md` inside
the bundle.

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

- The distributed images and rootfs are a **lean build**: agent CLIs
  (Claude Code / Codex / GitHub Copilot / Antigravity / OpenCode) are not bundled — on first start
  each user fetches verified, pinned versions from the respective upstream
  (this build intentionally avoids redistribution).
- For attribution of bundled OSS, see the `NOTICE` file inside each tar.
