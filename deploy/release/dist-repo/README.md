# agent-fleet-dist

English | [日本語](README.ja.md)

Distribution artifacts for [Agent Fleet](https://github.com/k-k1/agent-fleet).
**There is no source code here** — binaries and bundles are attached to Releases.

## What is Agent Fleet?

Agent Fleet is a self-hosted web console for running AI coding agents
(Claude Code, Codex CLI, OpenCode, GitHub Copilot CLI, Antigravity CLI) as a
managed fleet.
Each member gets an isolated workspace — a Docker container with cgroup CPU/memory
quotas (or a bubblewrap-sandboxed rootfs in the native edition) with a persistent
home and git working copies — and starts, drives and monitors agent sessions from
the browser. A Go control plane orchestrates the workspaces; deployment targets
include on-prem Docker Compose, AWS ECS (CloudFormation templates included), and a
Docker-less native runtime for WSL2 / single-user Linux hosts.

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
  (Claude Code / Codex / OpenCode / Copilot / Antigravity) are not bundled — on first start
  each user fetches verified, pinned versions from the respective upstream
  (this build intentionally avoids redistribution).
- For attribution of bundled OSS, see the `NOTICE` file inside each tar.
