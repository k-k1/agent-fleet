---
audience: "someone running Agent Fleet without Docker on a single-user Linux host (WSL2 included)"
source_of_truth: "`deploy/native/af` and the distribution repo's `install.sh` (a contradiction is a bug in this page)"
---

# Install the native package

English | [日本語](install.ja.md)

Install Agent Fleet on a single-user Linux host without Docker (WSL2 included) and start using it from the browser.
It runs without installing anything on the host: there is no need for tmux, git, node or the agent CLIs there.

## How it works

| What you do | What happens |
|---|---|
| Run the installer, or extract the tar | The `af` command is available |
| Run `af start` | The first time only, it fetches, verifies and extracts the rootfs (~200 MB). The Control Plane and the Console run as host processes |
| Open `http://localhost:8099` in a browser | The Console opens with no sign-in. Workspaces run in a bubblewrap sandbox |
| Start a workspace for the first time | The agent CLIs are installed, at their pinned versions, into the virtual HOME |

For several people, or for container isolation and CPU / memory limits, choose a Docker-based target ([Deploy with Docker Compose](../compose/install.md) and others).

## Before you start

| What you need | Notes |
|---|---|
| A Linux kernel (unprivileged user namespaces) | The stock WSL2 kernel has AppArmor disabled, so it should work as is. Ubuntu 23.10 and later may need the setting below |
| bash, coreutils, tar | Standard userland only. zstd is bundled as `bin/zstd`, so the host does not need it |
| curl or wget | Used only for the first rootfs download (see also [Carry it in by hand](update.md#carry-it-in-by-hand)) |
| (For `agy`) a CPU that exposes RDRAND | `agy`'s (the Antigravity CLI's) FIPS build aborts without RDRAND. Where the kernel has withdrawn the instruction, the Agent masks it out of OpenSSL's detection and runs agy anyway, its randomness coming from the kernel. `AF_AGY_RDRAND_MASK=0` refuses that, and the Console hides `agy` |

On a kernel with user namespaces disabled entirely, the native package cannot run: use the Docker target (`agent-fleet-<v>.tar.gz`).
There is no native package for macOS. On an Apple Silicon Mac, run it inside a Linux VM ([Run on an Apple Silicon Mac](../compose/macos.md#run-the-native-package-in-a-linux-vm)).

### When AppArmor blocks it on Ubuntu 23.10 or later

On bare-metal Ubuntu 23.10 or later (not WSL), AppArmor may restrict unprivileged user namespaces. In that case, run once:

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
# make it permanent:
echo 'kernel.apparmor_restrict_unprivileged_userns=0' | sudo tee /etc/sysctl.d/60-agent-fleet.conf
```

## 1. Install it

The installer from the distribution repo ([k-k1/agent-fleet-dist](https://github.com/k-k1/agent-fleet-dist)) extracts to `~/.local/opt/agent-fleet/<v>/` and symlinks `~/.local/bin/af`.

```bash
curl -fsSL https://raw.githubusercontent.com/k-k1/agent-fleet-dist/main/install.sh | bash
af start            # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

| Environment variable | Meaning |
|---|---|
| `AF_VERSION` | The version to install. The default is the latest release |
| `AF_PREFIX` | The install prefix. The default is `~/.local` |
| `AF_NO_AUTOUPDATE` | `1` skips enabling the daily update timer ([Update the native package](update.md)) |

If you received the tar directly, extract it and use the `af` inside. `<arch>` is `amd64` or `arm64`, matching `uname -m`'s `x86_64` / `aarch64`.

```bash
tar xzf agent-fleet-native-<v>-linux-<arch>.tar.gz
cd agent-fleet-native-<v>-linux-<arch>
./af start          # first run only: fetch, verify, and extract the rootfs (~200MB)
# open http://localhost:8099 in a browser
```

## 2. Start it and open it

- The first `af start` downloads the rootfs pinned in `rootfs.json` (URL and sha256). **Later starts work offline** (the extracted rootfs is reused)
- `af start` runs in the foreground; stop it with Ctrl-C. To run it as a service, see [Run it as a systemd service](service.md)
- Open `http://localhost:8099` in a browser. On WSL2 a browser on the Windows side opens it directly
- `af status` shows whether the Control Plane and the workspaces are running

There is no sign-in screen. It runs as one fixed `AUTH=dev` user, and that user is a deployment administrator (super_admin).
The tenant settings screen is reachable straight after `af start`.

### The agent CLIs

The agent CLIs are not in the package.

| CLI | When it is installed |
|---|---|
| claude, codex, opencode, cursor, copilot, agy, rtk | On a workspace's first start, the versions pinned in `versions.json` go into the virtual HOME. Only this step needs the network |
| kiro (~855 MB) | The first time a Kiro session starts |
| Muse Code | Not until you ask for it: its connection card's "**Install Muse Code**" fetches the pinned version into your home |

## Data locations

| Contents | Location |
|---|---|
| All data (DB, workspace homes, claude-config, extracted rootfs) | `~/.local/share/agent-fleet`. Override with `WS_DATA` |
| The package itself | The extracted directory. It holds no data, so it is safe to delete |

- The layout is compatible with the Docker targets, so the same `WS_DATA` can move between docker and native
- From Windows Explorer it is at `\\wsl.localhost\<distro>\home\<user>\.local\share\agent-fleet`

## Connect repositories through OAuth

To clone and push private repositories, connect GitHub or Bitbucket under **Settings → Git hosting** in the Console.
Pasting a token works with no configuration at all.

To also enable the one-click "Connect via OAuth", register the OAuth app in the Console's tenant settings ([Choose the OAuth apps for integrations](../../admin/access/oauth-apps.md)). There is no environment variable for it.

| Provider | What to enter | Setup |
|---|---|---|
| GitHub (device flow) | `client_id` only | Create an OAuth App under GitHub → Settings → Developer settings → OAuth Apps with **Enable Device Flow ON**. The `client_id` is not a secret. Device flow needs no callback URL, so it works as is on `localhost` |
| Bitbucket (authorization code) | Key and Secret | Create an OAuth consumer whose **Callback URL** exactly equals `<PUBLIC_BASE_URL>/api/oauth/bitbucket/callback`. Set `PUBLIC_BASE_URL` to how you reach the Console (e.g. `http://localhost:8099`). The screen shows the URL to paste |

For a single-user native install, GitHub's device flow is the easiest, since it needs no callback.

## Differences from the Docker targets

| Difference | What it means |
|---|---|
| One user only | Fixed to `AUTH=dev`. There is no container isolation: every workspace runs in a bubblewrap sandbox under the same OS user |
| No memory limits | No cgroups are used, so `WS_MEMORY` has no effect. A heavy build can take down the whole host |
| The browser pane's Chromium is fetched on first use | A pinned version, ~200 MB. See "When Chromium's sandbox does not work" below |

### When Chromium's sandbox does not work

Under bubblewrap the SUID sandbox is unavailable, and on some systems Chromium's namespace sandbox does not work either. In that case start with:

```bash
WS_ENV=AF_CHROMIUM_NO_SANDBOX=1 af start
```

- The variable reaches the workspace through `WS_ENV` (a comma-separated `KEY=VAL` list). Set bare on `af start`, it only reaches the Control Plane
- An acceptable trade-off for the browser pane, which only connects to localhost. Do not use it to browse untrusted sites

## What you can add

| What you want | Page |
|---|---|
| Read replies aloud (VOICEVOX) | [Run the read-aloud engine](../engines/read-aloud.md) |
| Generate images on a ComfyUI on your network | [Generate images on your own ComfyUI](../engines/comfyui.md) |
| Borrow the inference engines of an Agent Fleet on AWS | [Borrow another deployment's engines](../engines/borrow.md) |
| Chat on a llama.cpp on your LAN | [Chat on a llama.cpp on your LAN](../engines/llama-cpp.md) |

Each of them is an environment variable on `af start`, or an `Environment=` line in the systemd unit. `af` passes your shell environment through to the Control Plane.

## When it goes wrong

| Symptom | Where to look |
|---|---|
| `af start` stops on a user-namespace error | On Ubuntu 23.10 or later, the [AppArmor setting](#when-apparmor-blocks-it-on-ubuntu-2310-or-later). If the kernel disables them, use a Docker target |
| The browser pane's Chromium does not start | [When Chromium's sandbox does not work](#when-chromiums-sandbox-does-not-work) |
| Port 8099 is in use | Another `af start`, or the systemd service, is already running |

## Related

- [Update the native package](update.md)
- [Run it as a systemd service](service.md)
- [Reset or uninstall](uninstall.md)
- [Run it for yourself on WSL](../wsl.md)
