# Agent Fleet — native package (no Docker, single user)

Agent Fleet is a self-hosted web console for running AI coding agents (Claude Code,
Codex CLI, OpenCode, GitHub Copilot CLI, Antigravity CLI) as a managed fleet. This native package runs
it without Docker on a single-user Linux host (including WSL2): the control plane and
console run as host processes, and the workspace runs inside a bubblewrap
(user-namespace) sandbox on a downloaded rootfs.

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
- The agent CLIs (claude / codex / opencode / agy / copilot / rtk) are not baked in.
  On a workspace's first start, the entrypoint auto-installs the versions pinned in
  versions.json (verified-working versions) into the virtual HOME (network is needed
  only then).
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
  baked in — docs/35 §35.4.1).

## systemd user unit (run as a service; systemd is on by default in WSL2)

`~/.config/systemd/user/agent-fleet.service`:

```ini
[Unit]
Description=Agent Fleet (native)

[Service]
ExecStart=%h/agent-fleet/af start
Restart=on-failure

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-fleet
```

## Limitations (differences from the Docker setup)

- **Single user only** (AUTH=dev fixed). There is no container isolation; all
  workspaces run in bubblewrap sandboxes under the same OS user.
- **No memory limits** (no cgroups are used, so `WS_MEMORY` has no effect). Heavy
  builds can take down the whole host.
- **The browser pane's chromium is downloaded on first use** (~200MB, pinned
  version). The SUID sandbox is unavailable under bubblewrap, and on some systems
  chromium's namespace sandbox does not work either. In that case start with
  `AF_CHROMIUM_NO_SANDBOX=1` (a trade-off acceptable for the pane use case, which
  only connects to localhost; do not use it to browse untrusted sites).

## Reset

```bash
./af reset          # dev user's data only (DB and rootfs are kept)
./af reset --all    # all of WS_DATA (full wipe incl. the extracted rootfs)
```
