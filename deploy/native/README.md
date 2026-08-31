# Agent Fleet — native package (no Docker, single user)

Agent Fleet is a self-hosted web console for running AI coding agents (Claude Code,
Codex CLI, GitHub Copilot CLI, Antigravity CLI, Cursor CLI, Kiro, OpenCode) as a managed fleet. This native package runs
it without Docker on a single-user Linux host (including WSL2): the control plane and
console run as host processes, and the workspace runs inside a bubblewrap
(user-namespace) sandbox on a downloaded rootfs. For multi-user service with
container isolation and cgroup CPU/memory quotas, use the Docker-based editions
(Compose / AWS ECS) instead.

It runs on plain WSL2 (or any Linux, single-user operation) **without installing
anything on the host**. Workspaces run on the bundled rootfs (the same userland as
the workspace image) via unprivileged bubblewrap, so there is no need to install
tmux / git / node / the agent CLIs on the host.

## Quick start

One-liner install from the distribution repo
([k-k1/agent-fleet-dist](https://github.com/k-k1/agent-fleet-dist)) — extracts to
`~/.local/opt/agent-fleet/<v>/` and symlinks `~/.local/bin/af`:

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start            # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

If you received the tar directly:

```bash
tar xzf agent-fleet-native-<v>-linux-amd64.tar.gz
cd agent-fleet-native-<v>-linux-amd64
./af start          # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

- The first `af start` downloads the rootfs pinned in rootfs.json (URL + sha256).
  **Later starts work offline** (the extracted rootfs is reused).
- The agent CLIs (claude / codex / opencode / cursor / copilot / agy / rtk) are not baked in.
  On a workspace's first start, the entrypoint auto-installs the versions pinned in
  versions.json (verified-working versions) into the virtual HOME (network is needed
  only then). kiro (~855MB) is the exception: it is installed on demand the first
  time a Kiro session starts, not at workspace start.
- Stop with Ctrl-C (runs in the foreground). For running as a service, see the
  systemd user unit below.

## Host requirements

| Requirement | Notes |
|---|---|
| Linux kernel (unprivileged user namespaces) | The stock WSL2 kernel has AppArmor disabled, so it should work as-is. See the note below |
| bash / coreutils / tar | Standard userland only. zstd is bundled as `bin/zstd`, so it is **not needed on the host** |
| curl or wget | Used only for the initial rootfs download (see air-gap below) |
| (optional) CPU exposing RDRAND | Only for the `agy` (Antigravity CLI) agent kind — its FIPS build aborts without RDRAND. Hosts without it run everything else; the Console just hides `agy` |

On **bare-metal Ubuntu 23.10+ (non-WSL)**, unprivileged userns may be restricted by
AppArmor. In that case, run once:

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
# make it permanent:
echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/60-agent-fleet.conf
```

On kernels with userns disabled entirely, the native package cannot run (use the
Docker setup, `agent-fleet-<v>.tar.gz`, instead).

## Data locations

| Contents | Location |
|---|---|
| All data (DB / workspace homes / claude-config / extracted rootfs) | `~/.local/share/agent-fleet` (override with `WS_DATA`) |
| The package itself | This extracted directory (holds no data — safe to delete) |

The layout is compatible with the Docker setup (the same `WS_DATA` can move between
docker ⇄ native). From Windows Explorer it is reachable at
`\\wsl.localhost\<distro>\home\<user>\.local\share\agent-fleet`.

## Updating

```bash
tar xzf agent-fleet-native-<v'>-linux-amd64.tar.gz   # extract the new version
cd agent-fleet-native-<v'>-linux-amd64 && ./af start
```

- Data (`WS_DATA`) lives outside the package and is untouched. DB migrations run
  automatically at startup and are forward-only (**no downgrade support**). A tar
  backup of `WS_DATA` before updating is recommended.
- Releases that keep the same rootfs version `<r>` do not re-download it.
- Delete the old package directory once you no longer need it.

## Air-gap / manual file transfer

- The self-contained variant (the `-bundle` tar built with `--bundle-rootfs`) bundles
  the rootfs and starts without any download.
- With the regular variant, you can pass a rootfs tar fetched on another host via
  `./af start --rootfs <tar.zst>` (the same sha256 verification applies).
- Only the first-time CLI auto-install needs network access. If you need the CLIs
  fully offline, use a Docker setup you built yourself from the repo's Dockerfile
  with `BAKE_AGENT_CLIS=1` (licensing forbids redistributing builds with the CLIs
  baked in — docs/log/35 §35.4.1).

## Git provider OAuth (GitHub / Bitbucket) — optional

To clone/push private repos, each user connects their own GitHub or Bitbucket
from the Console (**⚙ Settings → Git hosting**). Pasting a Personal Access Token / app
token works with no configuration at all. To also enable the one-click **"Connect
via OAuth"** buttons, register your OAuth app **in the Console**:

**Tenant settings → Integrations → Git provider OAuth**

There is no environment variable for this any more (docs/log/71). `GITHUB_OAUTH_CLIENT_ID`
and `BITBUCKET_OAUTH_KEY`/`_SECRET` are not read: the app is a per-tenant row, so it
takes effect immediately and does not need workspaces restarted.

| Provider | What to enter | Setup |
|---|---|---|
| **GitHub** (device flow) | `client_id` only | Create an OAuth App (GitHub → Settings → Developer settings → OAuth Apps) with **"Enable Device Flow" ON**. The client_id is **not a secret**, and device flow needs **no callback URL**, so it works as-is on `localhost`. |
| **Bitbucket** (auth code grant) | Key + Secret | Create an OAuth consumer whose **Callback URL** exactly equals `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback`. Set `PUBLIC_BASE_URL` to how you reach the Console (e.g. `http://localhost:8099`) — the screen shows the exact URL to paste. |

A native install runs `AUTH=dev` (one fixed user, no sign-in), and that user is a
**super_admin**, so the tenant settings screen is reachable straight after `af start`
with nothing else to configure.

Token paste keeps working whether or not you do this, so OAuth is purely a
convenience. GitHub's device flow is the easiest path for a single-user native
install because it needs no callback.

## Text-to-speech (TTS / Zundamon) — optional

Reading chat replies aloud (enable it in the Console under Settings > "Read aloud";
off by default) needs no audio setup on the WSL/Linux side: **playback** happens in
your browser (on the Windows side under WSL2). All that is required is that the CP
can reach the **VOICEVOX** synthesis engine over HTTP (default
`http://127.0.0.1:50021`). The engine is not bundled with the package or the
rootfs. Provide it in either of the following ways (leaving it out does not affect
any other feature).

### A. Run VOICEVOX on the Windows side (WSL2)

Launching the [VOICEVOX](https://voicevox.hiroshiba.jp/) Windows app exposes the
same HTTP API at `127.0.0.1:50021` on the Windows side.

- With WSL2 in **mirrored networking mode** (`[wsl2]` → `networkingMode=mirrored`
  in `%UserProfile%\.wslconfig`, Windows 11 22H2+), WSL reaches it at
  `localhost:50021`, so it works **as-is with `af start` — no configuration**.
- In the default **NAT mode**, WSL cannot reach the Windows loopback. Run the
  standalone engine (the Windows build of VOICEVOX ENGINE) with
  `run.exe --host 0.0.0.0`, and point at the Windows host IP as seen from WSL
  (`ip route | awk '/^default/{print $3}'`) — Windows Firewall must allow inbound
  connections from the vEthernet (WSL) interface:

  ```bash
  AF_VOICEVOX_URL=http://<windows-host-ip>:50021 af start
  ```

### B. Run the engine directly inside WSL/Linux

Download and extract the Linux (CPU) build of
[VOICEVOX ENGINE](https://github.com/VOICEVOX/voicevox_engine) and start it with
`./run` (default `127.0.0.1:50021`, ~1GB resident, no Docker needed). The default
URL matches, so `af start` needs no changes.

### Changing the URL / port

The engine location is passed to the CP via the `AF_VOICEVOX_URL` env var (a full
URL including the port). `af` passes your shell environment through to the CP, so
just set it and start:

```bash
AF_VOICEVOX_URL=http://127.0.0.1:50022 af start
```

When running under systemd (below), add `Environment=AF_VOICEVOX_URL=...` to the
unit's `[Service]` section.

- While the engine is down, only speech fails (with the `auto` setting it falls
  back to AWS Polly if configured), and it recovers from the next sentence once
  the engine is up.
- To use AWS Polly, set `AF_POLLY_REGION` plus standard AWS credentials visible
  to the CP process (e.g. `~/.aws`).

## systemd user unit (run as a service; systemd is on by default in WSL2)

Instead of keeping `af start` in the foreground, run it as a systemd **user**
service (auto-restart, survives your shell). `~/.config/systemd/user/agent-fleet.service`:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/.local/bin/af start
Restart=on-failure
# Lets the Console's "restart to apply" button (auto-update, below) find this
# unit; %N expands to the unit name (agent-fleet).
Environment=AF_SYSTEMD_UNIT=%N

[Install]
WantedBy=default.target
```

`%h` expands to your home directory. The `ExecStart` path above matches the
**one-liner install** (which symlinks `~/.local/bin/af`). If you extracted the
tar by hand instead, point it at that copy — e.g.
`ExecStart=%h/agent-fleet-native-<v>-linux-amd64/af start` (use
`readlink -f "$(command -v af)"` to find the real path).

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
systemctl --user status agent-fleet          # expect: Active: active (running)
```

- Stop the foreground `af start` first if it is still running — otherwise the
  service fails to bind port 8099 (double start).
- To keep it running after you close the WSL session:
  `loginctl enable-linger "$USER"`.
- Logs: `journalctl --user -u agent-fleet -f`.
- If `systemctl --user` reports `Failed to connect to bus`, user systemd is not
  running — on WSL2 enable it via `/etc/wsl.conf` (`[boot]` / `systemd=true`),
  then `wsl.exe --shutdown` and reopen.

## Automatic updates

`af update` fetches the latest release, verifies it (sha256) and stages it beside
the current version, then re-points `~/.local/bin/af`. It **never restarts a
running control-plane** — the new version takes effect only when you restart, so
live agent sessions are never dropped without your say-so. User data under
`WS_DATA` is untouched; a new workspace image (rootfs) is fetched lazily on the
next start.

```bash
af update --check   # report whether a newer release exists (no download)
af update           # download + verify + stage the latest (or AF_VERSION-pinned)
```

The one-liner installer enables a **daily user timer** that runs `af update`
automatically (stage only). Opt out at install time with `AF_NO_AUTOUPDATE=1`, or
later:

```bash
systemctl --user disable --now agent-fleet-update.timer   # stop auto-staging
systemctl --user list-timers agent-fleet-update.timer     # when it next runs
```

**Applying a staged update** (picking up the new version) is a restart of the
main service, which you trigger when convenient:

- **From the Console** — when a newer version is staged, a "restart to apply"
  control appears (Settings → Toolchains). It warns how many sessions are running before
  it restarts, so you can wait until the fleet is idle.
- **From the shell** — `systemctl --user restart agent-fleet` (or Ctrl-C + `af
  start` for a foreground run).

To **pin** a version (and stop auto-updates advancing past it), set
`Environment=AF_VERSION=<v>` in *both* the `agent-fleet` and
`agent-fleet-update` units; `af update` then treats that version as the target
and does nothing once you are on it.

> Restarting the service stops the control-plane and the workspaces it supervises
> (under systemd the whole unit cgroup is stopped), so in-flight agent sessions
> are interrupted. That is why applying is a deliberate action, not automatic.

## Limitations (differences from the Docker setup)

- **Single user only** (AUTH=dev fixed). There is no container isolation; all
  workspaces run in bubblewrap sandboxes under the same OS user.
- **No memory limits** (no cgroups are used, so `WS_MEMORY` has no effect). Heavy
  builds can take down the whole host.
- **The browser pane's chromium is downloaded on first use** (~200MB, pinned
  version). The SUID sandbox is unavailable under bubblewrap, and on some systems
  chromium's namespace sandbox does not work either. In that case start with
  `WS_ENV=AF_CHROMIUM_NO_SANDBOX=1 af start` — the variable must be forwarded into
  the workspace via `WS_ENV` (a comma-separated `KEY=VAL` list); setting it bare on
  `af start` only reaches the control plane, not the workspace. (A trade-off
  acceptable for the pane use case, which only connects to localhost; do not use it
  to browse untrusted sites.)

## Reset

```bash
./af reset          # dev user's data only (DB and rootfs are kept)
./af reset --all    # all of WS_DATA (full wipe incl. the extracted rootfs)
```

## Uninstall

```bash
# 1. If you set up the systemd user unit, stop and remove it first
systemctl --user disable --now agent-fleet 2>/dev/null
systemctl --user disable --now agent-fleet-update.timer 2>/dev/null   # auto-update timer
rm -f ~/.config/systemd/user/agent-fleet.service \
      ~/.config/systemd/user/agent-fleet-update.service \
      ~/.config/systemd/user/agent-fleet-update.timer
# otherwise just stop `af start` with Ctrl-C

# 2. Wipe all data (skip to keep it — data is separate from the program)
./af reset --all

# 3. Remove the program
#    installed via install.sh:
rm -f ~/.local/bin/af && rm -rf ~/.local/opt/agent-fleet
#    extracted from a tar directly: just delete the extracted directory
```

If `af` is already gone, remove the data manually — Go module caches inside
workspace homes are write-protected, so restore write permission first:
`chmod -R u+w ~/.local/share/agent-fleet && rm -rf ~/.local/share/agent-fleet`
(use your `$WS_DATA` path if you overrode it).

## Disclaimer — autonomous agent execution

The agents run commands, edit files, and commit/push on your behalf — including
**unattended** (scheduled runs that wake a stopped workspace), in
**permission-bypassing modes**, and through **shell / SSM sessions that run the
strings you send verbatim**. Such actions can be destructive or irreversible and can
incur charges on your AI-provider and cloud accounts. You are solely responsible for
the workspaces, credentials, repositories, and infrastructure you connect, and for
reviewing what the agents do; use least-privilege credentials, keep backups, and
prefer the approval gates for destructive actions. This software is distributed under
the **Apache License 2.0** and is provided **"AS IS", WITHOUT WARRANTIES OR CONDITIONS
OF ANY KIND**; the authors accept **no liability** for any damage, data loss, or cost
arising from its use. See `LICENSE`, and `NOTICE` for bundled-OSS attribution.

Official releases come only from <https://github.com/k-k1/agent-fleet-dist>. If you
pass this package on, Apache-2.0 §4(d) requires you to keep the notices in `NOTICE`
with it — that URL included — so the next recipient can find the original.
