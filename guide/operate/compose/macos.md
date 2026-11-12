---
audience: "someone running Agent Fleet for one person on an Apple Silicon Mac"
source_of_truth: "`deploy/compose/docker-compose.macos.yml` and the checks in `deploy/macos/acceptance.sh` (a contradiction is a bug in this page)"
---

# Run on an Apple Silicon Mac

English | [日本語](macos.ja.md)

Run Agent Fleet for one person on an Apple Silicon Mac.
You can run it on a Docker engine (colima, OrbStack, Docker Desktop), or run the native package inside a Linux VM.

## How it works

| What you do | What happens |
|---|---|
| Write the Mac values into `.env` | The Control Plane starts for one user (`AUTH=dev`) |
| Start with the overlay file `docker-compose.macos.yml` added | The Control Plane runs on a bridge network and reaches workspaces through `host.docker.internal` |
| Open `http://127.0.0.1:8099` in the Mac's browser | The Console opens and you can launch a workspace |

What is covered and what is not:

| Covered | Not covered |
|---|---|
| One person on an Apple Silicon Mac | Intel Macs (never run) |
| colima, OrbStack, Docker Desktop | A team or public deployment (this setup has no Caddy and no IdP) |
| The native package inside a Lima or OrbStack machine | Apple's `container` tool (it is not a Docker daemon, so the Control Plane cannot drive it) |

## Run it on a Docker engine

The images are published for `linux/arm64` too, so the Mac pulls the right ones. What differs from Linux is the wiring for driving Docker from a container.

On each of colima, OrbStack and Docker Desktop these were checked: starting a workspace, `linux/arm64`, home ownership and modes on the shared directory, headless Chromium, and stop → start keeping the home.

- colima was started with `colima start --vm-type vz --mount-type virtiofs`
- OrbStack and Docker Desktop have to be started once from a logged-in screen. After that the commands below work from any terminal

### 1. Write `.env`

Besides the secrets from [Deploy with Docker Compose](install.md#1-configure-and-start-from-source), write:

```bash
AUTH=dev                                  # one fixed user, no IdP
AF_ALLOW_INSECURE_DEV_AUTH=1              # required: the CP refuses AUTH=dev on Docker without it
AF_PROVISION=auto                         # the template's `invite` leaves that user in no tenant
PUBLIC_BASE_URL=http://127.0.0.1:8099
AF_TRUSTED_PROXY_HOPS=0                   # no Caddy in front; the template's 1 assumes one
VERSION=<version>                         # the template's `dev` is not a published CP tag
WS_IMAGE=ghcr.io/k-k1/agent-fleet/workspace:<version>
# (B) a directory the engine shares with its VM — under your home works for all three
DATA_DIR=/Users/<you>/agent-fleet/data
# (C) the socket's group as the engine reports it, not a host `docker` group
#     (measured: 991 on colima, 0 on OrbStack and Docker Desktop):
#   docker run --rm -v /var/run/docker.sock:/var/run/docker.sock alpine stat -c %g /var/run/docker.sock
DOCKER_GID=<that number>
```

| Value | Why |
|---|---|
| `DATA_DIR` | A directory the engine shares with its VM. Under your home it is shared on all three engines |
| `DOCKER_GID` | The socket's group as the engine reports it, not the host's `docker` group. It was 991 on colima and 0 on OrbStack and Docker Desktop. Measure it again for each engine |

### 2. Start with the overlay

```bash
docker compose -f docker-compose.yml -f docker-compose.macos.yml up -d
```

Open `http://127.0.0.1:8099` in the Mac's browser.

### What the overlay changes

Of the [three constraints of driving Docker from a container](../diagnose/checklist.md#three-constraints-of-driving-docker-from-a-container), these are the ones that change on a Mac.

| Constraint | On a Mac |
|---|---|
| A. The host network | On a Mac the daemon runs in a Linux VM, so "host" is the VM. The overlay puts the Control Plane on a bridge network, reaches workspaces through `host.docker.internal` (`WS_AGENT_HOST`), and gives workspaces the [workspace listener](workspace-listener.md) under the same name, because `127.0.0.1` inside a workspace is the workspace itself. Both ports are published on the Mac's loopback only |
| B. The absolute path of `DATA_DIR` | The home lives on a directory the engine shares with its VM. With colima and virtiofs, files kept uid 1000 and their modes (`~/.ssh` 700, a new file 600), and writes were not slower than the container's own disk. Another engine's file sharing may behave differently |
| C. The user and the docker group | `DOCKER_GID` above |

There is an unchecked alternative: Docker Desktop 4.34 and later can enable host networking (Settings → Resources → Network), and OrbStack supports `network_mode: host`. With it, the plain `docker-compose.yml` might work unchanged.

## Run the native package in a Linux VM

macOS has neither bubblewrap nor Linux user namespaces, and the workspace rootfs is Linux. So there is no macOS package, and `install.sh` refuses to run on a Mac.

1. Prepare a Linux VM (Lima, an OrbStack machine (`orb create ubuntu`), …)
2. Inside the VM, install the `linux-arm64` package and run `af start` ([Install the native package](../native/install.md))
3. Open `http://localhost:8099` in the Mac's browser through the VM's port forward

- Install, start, workspace start and stop → start were checked with Lima's Ubuntu LTS template and with an OrbStack machine. An OrbStack machine's ports also reach the Mac's `localhost`
- An Ubuntu VM restricts unprivileged user namespaces like any Ubuntu 23.10 or later. Apply the [AppArmor setting](../native/install.md#when-apparmor-blocks-it-on-ubuntu-2310-or-later) first

## When it goes wrong

| Symptom | Where to look |
|---|---|
| The Control Plane refuses `AUTH=dev` and does not start | `AF_ALLOW_INSECURE_DEV_AUTH=1` |
| You sign in but belong to no tenant | `AF_PROVISION=auto` |
| "permission denied" on the docker socket | Did you measure `DOCKER_GID` again on the engine you use? |
| After switching engines you see the previous data, or the ports clash | Give each engine its own `DATA_DIR`, and stop the previous engine before switching with `docker context use <name>` |

## Related

- [Deploy with Docker Compose](install.md)
- [When workspaces cannot reach the CP](workspace-listener.md)
- [Install the native package](../native/install.md)
