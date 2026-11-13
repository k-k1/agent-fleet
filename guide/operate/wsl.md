---
audience: "someone trying Agent Fleet alone on Windows WSL2"
source_of_truth: "`deploy/local/run-dev.sh` (a contradiction is a bug in this page)"
---

# Run it for yourself on WSL

English | [日本語](wsl.ja.md)

Bring Agent Fleet up with one command on Windows WSL2, for one person to try it.
There is no sign-in and no tenant picker, and workspaces run in Docker inside WSL.

## How it works

| What you do | What happens |
|---|---|
| Install Docker Engine, Go and Node in the WSL2 distro | The startup script can build |
| Run `deploy/local/run-dev.sh wsl` | It builds the workspace image, the Console and the Control Plane, and starts the Control Plane on the host |
| Open `http://localhost:8099` in a Windows browser | The Console opens with no sign-in |

The production Docker Compose and Caddy setup needs a public domain and ports 80/443, so it is not used for trying it alone.

### What is left out

| Item | In this setup |
|---|---|
| Sign-in | The fixed `AUTH=dev` user `dev`. There is no sign-in screen and no oauth2-proxy; every request resolves to `dev`. The script passes `AF_ALLOW_INSECURE_DEV_AUTH=1` as well |
| Tenants | A `default` tenant is created internally and `dev` joins it automatically. With one membership, the tenant picker never appears |
| Encryption | `AF_MASTER_KEY` is not set, so at-rest encryption is off: secrets are stored as plain JSON. Acceptable for trying it alone |
| Runtime | `AF_RUNTIME=local`. Workspace containers start in Docker inside WSL, with container isolation and per-workspace cgroup limits, as on the production targets |

No code changes are needed: all of the above is what `run-dev.sh wsl` does by default.

## 1. Install the prerequisites

Install them inside the WSL2 distro. For Docker, **Docker Engine installed directly inside WSL2** is recommended.
Docker Desktop's integration also works, but this setup assumes the same namespaces and paths as the host Docker, so the direct install fits better.

| What to install | Notes |
|---|---|
| Docker Engine (dockerd) | With the commands below |
| cgroup v2 | The `--memory` cap and the resource display depend on it. Recent WSL2 defaults to v2 |
| Go | Builds the Control Plane on the host. Install from https://go.dev/dl/ and add it to PATH |
| Node | Builds the Console with Vite. nvm is recommended |

```bash
curl -fsSL https://get.docker.com | sh          # install docker-ce
sudo usermod -aG docker "$USER"                 # docker without sudo from now on
sudo service docker start                        # on WSL, `service` works even without systemd
# open a new shell: you're set once `docker info` succeeds
```

- With systemd enabled (`systemd=true` under `[boot]` in `/etc/wsl.conf`), `dockerd` starts by itself and you skip the manual step

```bash
stat -fc %T /sys/fs/cgroup     # => cgroup2fs means OK
```

- If it does not print `cgroup2fs`, run `wsl --update` on the Windows side to update WSL

```bash
curl -fsSL https://raw.githubusercontent.com/nvm-sh/nvm/v0.40.1/install.sh | bash
. ~/.nvm/nvm.sh && nvm install --lts
```

## 2. Start it

```bash
git clone <this-repo> && cd agent-fleet
deploy/local/run-dev.sh wsl        # the old wsl-quickstart.sh wraps this (still works)
```

What the script does:

1. Preflight checks (docker reachability, cgroup v2, go, npm)
2. Extracts the shared JDKs once into `~/.local/share/agent-fleet/shared/jvm` and serves them through a bind mount (skip with `WS_JDK=0`)
3. Builds the workspace image
4. Builds the Console (Vite) and the Control Plane (Go)
5. Starts the Control Plane with `AUTH=dev` and `AF_RUNTIME=local`

- The first run takes a while for the image build and the JDK extraction
- The image is the lean one, without the agent CLIs. A workspace's first start installs the pinned versions. `BAKE_AGENT_CLIS=1` bakes them in for a faster first start
- The old `deploy/local/wsl-quickstart.sh` is a compatibility wrapper that calls the same thing

`run-dev.sh` takes a subcommand:

| Subcommand | When |
|---|---|
| `wsl` | This setup |
| `local` | The development default (Docker) |
| `native` | When Docker cannot be installed ([When you cannot install Docker](#when-you-cannot-install-docker) below) |
| `reset` | Wipe the data ([Stop, wipe, rebuild](#stop-wipe-rebuild) below) |

## 3. Open it in a browser

The Control Plane listens on `http://localhost:8099`. Thanks to WSL2's localhostForwarding, **a browser on the Windows side** opens it directly.
From there, clone a repository in the Console and start a Claude session.

## Connect GitHub

For GitHub clone and push through the OAuth device flow, register a GitHub OAuth App's `client_id` in the Console. No environment variable is involved.

1. Create an OAuth App on GitHub (Settings → Developer settings → OAuth Apps) and turn **Enable Device Flow ON**
2. Register that `client_id` in the Console's tenant settings ([Choose the OAuth apps for integrations](../admin/access/oauth-apps.md)). The `client_id` is not a secret
3. Start the GitHub integration from a workspace: it walks you through a device code and a verification URL. `gh auth login` is not needed

- This setup is fixed to one user, and `AUTH` is always `dev`. Writing `AUTH=oauth` into `oauth.env` is not honored
- The fixed user is a deployment administrator (super_admin), which is what makes the tenant settings screen reachable
- Pasting a token (PAT) works too, and then nothing has to be registered

## rtk and JDKs

| Item | How it is handled |
|---|---|
| rtk (the token-saving Bash hook) | Installed at the pinned version when a workspace starts (baked into the image with `BAKE_AGENT_CLIS=1`). The entrypoint seeds the hook |
| JDKs | By default a **shared bind mount** (`WS_JVM_DIR`) that keeps the image slim |

Any environment can also install a JDK from inside the container:

```bash
workspace-agent install-jdk 21     # install the latest GA Temurin into ~/.local/share/agent-fleet/jvm
```

- Selecting a Java version under **Settings → Toolchains** in the Console makes the next container start install any missing version and export `JAVA_HOME` into each session ([Choose your toolchains](../user/workspace/toolchains.md))
- Starting with `WS_JDK=0` skips the shared JDKs and relies on this install alone
- The setting that keeps the agent CLIs and rtk on the latest version is on the same tab ([Keep the agent CLIs on the latest version](../user/workspace/toolchains.md#keep-the-agent-clis-on-the-latest-version))

## Stop, wipe, rebuild

| What you want | What to do |
|---|---|
| Stop | `Ctrl-C` the foreground Control Plane. Started workspace containers stay up; stop them with `docker ps` / `docker stop <name>` if needed |
| Where the data is | `~/.local/share/agent-fleet` (DB, per-user homes, shared JDKs) |
| Wipe | Stop the Control Plane, then `deploy/local/run-dev.sh reset`. By default it deletes only `dev`'s workspace (home and claude-config) and keeps the DB and the shared JDKs. `--all` deletes the DB and the shared JDKs too (the next run provisions the JDKs again) |
| Rebuild | After bumping CLI versions and the like, run `run-dev.sh wsl` again. The image is rebuilt |

`reset` cleans up what either runtime left behind (containers, Agent processes, the dedicated tmux) before deleting, so it is safe whichever form you ran.

## When you cannot install Docker

On a WSL2 where Docker really cannot be installed, workspaces can run as host processes without containers (`AF_RUNTIME=native`, single user, experimental).

```bash
# install CLIs like tmux / git / claude on the host (nothing is baked in)
deploy/local/run-dev.sh native
```

- There is no container isolation, no memory cap and no entrypoint initialization (the automatic claude install and the like). That is a deliberate trade-off
- The workspace HOME is kept apart at `~/.local/share/agent-fleet/dev/home` (your real `~` stays untouched), and Windows Explorer reaches it at `\\wsl.localhost\<distro>\...`
- If Docker is an option, prefer the setup in [1. Install the prerequisites](#1-install-the-prerequisites)
- To go without Docker and without the build tools, there is also the [native package](native/install.md)

## When it goes wrong

| Symptom | Where to look |
|---|---|
| `cannot reach the docker daemon` | `sudo service docker start` / `usermod -aG docker`, then log in again |
| A warning that cgroup is not v2 | Run `wsl --update` on the Windows side to update the WSL kernel |
| The Console build runs out of memory | `NODE_OPTIONS=--max-old-space-size=3072` (the script already sets it). Stop other builds when memory is tight |
| `go` or `npm` is missing | Install them as in [1. Install the prerequisites](#1-install-the-prerequisites) and fix PATH (the script sources nvm by itself) |
| Java is not found | `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk*`; if empty, `workspace-agent install-jdk <major>` |
| `agy` is missing from the agent picker | The host CPU exposes no usable RDRAND (`grep -w rdrand /proc/cpuinfo` is empty) and the workaround did not take. agy is a FIPS build that needs the instruction; where the kernel has withdrawn it, the Agent masks it out of OpenSSL's detection and runs agy anyway. If it is still hidden, the masked `agy --version` failed too, or `AF_AGY_RDRAND_MASK=0` refuses the mask |

## Related

- [Install the native package](native/install.md)
- [Deploy with Docker Compose](compose/install.md)
- [Choose where to deploy](choose.md)
