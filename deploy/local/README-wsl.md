# Personal use on WSL (no auth, single user, instant start)

Agent Fleet is a self-hosted web console for running AI coding agents (Claude Code,
Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI, Kiro, OpenCode) as a managed fleet — each member gets an
isolated workspace with a persistent home, and drives agent sessions from the browser.

This runbook gets agent-fleet up quickly on Windows **WSL2** for **single-person
evaluation**. There is no login screen and no tenant picker (`AUTH=dev` with the fixed
user `dev`), and workspaces run in Docker inside WSL (container isolation +
per-workspace cgroup quotas, same as the production targets). The production Compose + Caddy
(auto-TLS) setup (`deploy/compose/`) needs a public domain and ports 80/443, so it is
not used for personal evaluation.

Startup is a single command: `deploy/local/run-dev.sh wsl` (the old `wsl-quickstart.sh`
is a backward-compat wrapper that calls the same thing). The rest of this document sets
up its prerequisites.

---

## 0. The big picture (what is "left out")

- **Auth**: `AUTH=dev` (default). No OAuth gate, no oauth2-proxy — every request
  resolves to the fixed user `dev`. No login needed.
- **Tenants**: a single "default" tenant is auto-created internally and `dev` joins it
  automatically. You never deal with tenants in the UI (selection only appears with
  multiple memberships, so a single user is always auto-selected).
- **Encryption**: `AF_MASTER_KEY` unset → at-rest encryption off (secrets stored as
  plain JSON). Acceptable for personal evaluation.
- **Runtime**: `AF_RUNTIME=local` → workspace containers start in Docker inside WSL.

No code changes are needed; all of the above is the default behavior of `run-dev.sh` /
`wsl-quickstart.sh`.

## 1. Prerequisites (install inside the WSL2 distro)

The recommended setup is **native Docker Engine installed directly inside the WSL2
distro** (Docker Desktop integration also works, but instead of using `network_mode`
this setup assumes the same namespaces and paths as the host Docker, so native is the
simpler fit).

- **Docker Engine (native dockerd)**
  ```bash
  curl -fsSL https://get.docker.com | sh          # install docker-ce
  sudo usermod -aG docker "$USER"                 # docker without sudo from now on
  sudo service docker start                        # on WSL, `service` works even without systemd
  # open a new shell: you're set once `docker info` succeeds
  ```
  Enabling systemd auto-starts `dockerd` and saves the manual step (`[boot]\nsystemd=true`
  in `/etc/wsl.conf`).
- **cgroup v2** (the `--memory` cap and resource display depend on it)
  ```bash
  stat -fc %T /sys/fs/cgroup     # => cgroup2fs means OK
  ```
  Recent WSL2 defaults to v2. If yours is older, update WSL (`wsl --update`).
- **Go** (host-builds the Control Plane) … install from https://go.dev/dl/ and add to
  PATH.
- **Node** (Vite build of the Console) … nvm recommended.
  ```bash
  curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
  . ~/.nvm/nvm.sh && nvm install --lts
  ```

## 2. Start

```bash
git clone <this-repo> && cd agent-fleet
deploy/local/run-dev.sh wsl        # the old wsl-quickstart.sh wraps this (still works)
```

Startup scripts are unified into `run-dev.sh`; pick the form with a subcommand:
`local` (dev default, Docker) / `wsl` (this preset) / `native` (no Docker, §6) /
`reset` (wipe data, §5).

What the script does:
1. Preflight checks (docker reachability, cgroup v2, go, npm).
2. Extracts the shared JDKs once into `~/.local/share/agent-fleet/shared/jvm` and
   serves them via bind mount (skippable with `WS_JDK=0`, §4).
3. Builds the workspace image (rtk is always included in every build).
4. Builds the Console (Vite) and Control Plane (Go).
5. Starts the CP with `AUTH=dev` / `AF_RUNTIME=local`.

The first run takes a while for the image build (baking Chromium and the CLIs) and the
JDK extraction.

### GitHub integration (device flow)

To let GitHub clone/push go through the OAuth device flow, provide
`GITHUB_OAUTH_CLIENT_ID` (the client_id is **not a secret**; it is all the device flow
needs).

1. Create an OAuth App on GitHub (Settings → Developer settings → OAuth Apps) and turn
   **"Enable Device Flow" ON**.
2. Copy the template and fill in the client_id (the file is git-ignored):
   ```bash
   cp deploy/local/oauth.env.example deploy/local/oauth.env
   # edit deploy/local/oauth.env and set GITHUB_OAUTH_CLIENT_ID=<your-client-id>
   ```
3. Re-run `run-dev.sh wsl`. On startup it auto-sources `deploy/local/oauth.env` and
   injects `GITHUB_OAUTH_CLIENT_ID` into workspaces via the CP (the startup log shows
   `loaded .../oauth.env`).
4. Register your GitHub OAuth App in the Console under **Tenant settings ->
   Integrations -> Git provider OAuth** (client_id only — the app needs "Enable Device
   Flow" ON). Then start the GitHub integration from a workspace: it walks you through
   a device code and verification URL. `gh auth login` is not needed (the
   transparent-auth wrapper injects the token).

This WSL preset is fixed to a single user, so **`AUTH` is always `dev`** (writing
`AUTH=oauth` into `oauth.env` is not honored = login auth never changes). That fixed
user is a **super_admin**, which is what makes the tenant settings screen above
reachable. The git providers' OAuth apps are no longer read from `oauth.env` at all
(docs/log/71); token paste (PAT) also works, in which case nothing has to be registered.

## 3. Open in a browser

The CP listens on `http://localhost:8099`. Thanks to WSL2 localhostForwarding you can
open **`http://localhost:8099` directly from a browser on the Windows side**. From
there, clone a repository in the Console and start a Claude session.

## 4. rtk and JDKs

- **rtk** (the token-saving Bash hook): **always baked into the image** (pinned by
  `ARG RTK_VERSION` in `workspace/Dockerfile`, downloaded at build time). It is a
  single static binary, so the impact on image size is negligible. The entrypoint
  auto-seeds the hook. Turning ON the settings-modal option "update claude / opencode /
  codex / rtk to latest at startup" also updates rtk to the latest version on every
  boot (turn it OFF and Stop → Start to return to the baked version).
- **JDKs**: the default is a **shared bind mount** (`WS_JVM_DIR`) that keeps the image
  slim. In addition, any environment can install one from inside the container:
  ```bash
  workspace-agent install-jdk 21     # install the latest GA Temurin into ~/.local/share/agent-fleet/jvm
  ```
  **Selecting a Java version in the Console's toolchains settings** makes the
  entrypoint install any missing version into that location at the next container
  start and export `JAVA_HOME` into each session. Starting with `WS_JDK=0` skips the
  shared provision and relies on this on-demand install alone.

## 5. Stop, clean up, rebuild

- Stop: `Ctrl-C` the foreground CP. Started workspace containers stay up; use
  `docker ps` / `docker stop <name>` if needed.
- Data: persisted under `~/.local/share/agent-fleet` (DB, per-user homes, shared JDKs).
- Wipe: stop the CP, then `deploy/local/run-dev.sh reset`. The default deletes only
  the dev user's workspace (home / claude-config), keeping the DB and shared JDKs.
  `--all` is a full wipe including the DB and shared JDKs (the next run re-provisions
  the JDKs). It cleans up leftovers from either runtime (container, agent process,
  dedicated tmux) before deleting, so it is safe no matter which form you ran.
- Rebuild: after bumping CLI versions etc., re-run `run-dev.sh wsl` (rebuilds the
  image).

## 6. If you cannot install Docker (experimental: native runtime)

On WSL2 where Docker really cannot be installed, workspaces can run as **host
processes** without containers (`AF_RUNTIME=native`, single-user only, experimental):

```bash
# install CLIs like tmux / git / claude on the host (nothing is baked in)
deploy/local/run-dev.sh native
```

This is a deliberate trade-off: no container isolation, no memory caps, no entrypoint
initialization (automatic claude install etc.). Details and constraints:
[docs/log/34-native-runtime.md](../../docs/log/34-native-runtime.md). The workspace HOME is
isolated at `~/.local/share/agent-fleet/dev/home` (your real `~` stays untouched) and
is browsable from Windows Explorer at `\\wsl.localhost\<distro>\...` (docs/log/34 §34.4).
If Docker is an option, prefer the §1 setup (`wsl-quickstart.sh`).

## 7. Troubleshooting

| Symptom | Check |
|------|------|
| `cannot reach the docker daemon` | `sudo service docker start` / `usermod -aG docker`, then log in again |
| warning that cgroup is not v2 | update the WSL kernel with `wsl --update` (on the Windows side) |
| Console build OOMs | `NODE_OPTIONS=--max-old-space-size=3072` (the script already sets it). Stop other builds when memory is tight |
| `go`/`npm` missing | install per §1 and fix PATH (the script auto-sources nvm) |
| Java not found | `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk*`; if empty, `workspace-agent install-jdk <major>` |
| `agy` missing from the agent picker | the host CPU does not expose RDRAND (`grep -w rdrand /proc/cpuinfo` is empty). agy is a FIPS build that requires RDRAND, so it is deliberately hidden ([0008](../../docs/decisions/0008-antigravity-cli-agent-kind.md)) |

For deployment forms and the env index see [docs/build/09-deploy.md](../../docs/build/09-deploy.md);
for production Compose steps see [deploy/compose/README.md](../compose/README.md).
