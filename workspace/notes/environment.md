# This Workspace: persistence, disk, resources, what is missing, where the docs are

Read when: a recreate / clean-home / stop is coming up, `$AF_WS_SCRATCH` is set, a build died
with exit 137, you need a tool that is not installed, or the user asks how this environment
behaves. The always-loaded policy is `/etc/claude-code/CLAUDE.md`; this file is the long form.

## Persistence model (what survives what)

- **Recreate** (Settings > Environment) tears the container down and starts a fresh one from the
  latest image. **Only `~/repos` is deleted** — every cloned repo, *including uncommitted work*.
  That is the one data-loss risk: commit / push before recreating.
- **The rest of `~` persists** on a bind-mounted home volume that is re-attached to the new
  container: agent auth and state (`~/.claude`, `~/.codex`, `~/.local/share/opencode`,
  `~/.config/agent-fleet`), `~/.ssh` / `~/.git-credentials` / `~/.gitconfig`, tools under
  `~/.local` (the auto-updating `claude`, nvm node, `pip --user`), and the package caches
  (`~/.npm`, `~/.gradle`, `~/.m2`, `~/.cargo`, `~/go/pkg/mod`, `~/.cache/*`). Logins stay intact,
  and a re-install after a recreate is cheap because the caches are still warm.
- **Everything outside home is ephemeral** — `/`, `/usr`, `/opt`, `/tmp` revert to the image.
  Persist tools in `~/.local`. (You cannot `apt install` in the first place — no root.)
- **Claude's own state is on a separate mount and nothing deletes it**:
  `CLAUDE_CONFIG_DIR=/var/lib/af/claude`, including saved memory under `.../projects/*/memory/`,
  survives Stop/Start, recreate (touches only `~/repos`) and "clean home" (touches only home).
- **Some dotfiles may be symlinks.** Where the deployment keeps home on a per-user disk, the
  auth/connection set (`~/.config`, `~/.ssh`, `~/.git-credentials`, `~/.gitconfig`, `~/.claude`,
  `~/.claude.json`, `~/.codex`) lives on always-available storage and is linked into `~`, so
  losing the home disk never costs you your logins. Use them normally, but **never "repair" those
  links into real copies** — that puts them back on the disk the arrangement protects you from.

## `/scratch`: task-local disk (only when `$AF_WS_SCRATCH` is set)

Everything under it is gone as soon as the Workspace **stops** — not just on recreate. It exists
because `~` is network storage on that deployment (~9x slower for many small files), so only
regenerable caches live there (Go build cache, Go modules, `uv`); `~/.npm` deliberately stays on
home so a rebuild needs no network. Nothing in this section applies when `$AF_WS_SCRATCH` is
unset — there is no working disk and every path stays where you put it.

- **Build artifacts are relocated for you the moment a working copy is created**: `node_modules`
  (next to a `package.json`), `target` (`Cargo.toml`/`pom.xml`), `.venv` (`pyproject.toml`) and
  `build` (`build.gradle`) become symlinks into `/scratch` *before* anything installs into them —
  that first `npm ci` is exactly the cost being avoided. So an empty `node_modules` symlink in a
  fresh checkout is expected, not a broken install; but `[ -d node_modules ] || npm install` now
  thinks the install happened, so **run installs unconditionally**. Anything git tracks is never
  moved.
- Move one yourself any time: `af-scratch node_modules` (`af-scratch --status` lists what is
  relocated). Build output only (`node_modules`, `target`, `dist`, `.venv`) — **never tracked
  files or uncommitted work**, which an ordinary stop destroys.

## Memory / CPU — how to check your own numbers

Memory and CPU are per-workspace limits set by the deployment/tenant, so check them live. This is
a **cgroup v2** container: read *your own* numbers from inside and do NOT trust `free` or
`/proc/meminfo`, which show the whole shared HOST.

- Limit / current use: `cat /sys/fs/cgroup/memory.max` (bytes; `max` = uncapped) and
  `memory.current`. Human-readable:
  `awk '{printf "%.1f GiB\n",$1/1073741824}' /sys/fs/cgroup/memory.max`.
- Pressure: `cat /sys/fs/cgroup/memory.events` — rising `high`/`max`/`oom` means you are hitting
  the cap (the kernel throttles, then OOM-kills; a build dying with code 137 = OOM-killed).
- Cores: `nproc` (or `cat /sys/fs/cgroup/cpu.max`).
- The Console shows the same live (WS-bar resource chip, Settings > Environment). Near the cap,
  apply the build rules in `/usr/local/share/agent-fleet/notes/build.md`.

## Disk

- **Check before a big install** — `df -h ~`. The volume is shared with everything else you do,
  and caches grow without bound (`~/.npm`, `~/.cache` reach tens of GB).
- N worktrees means N copies of every per-project dependency tree unless the ecosystem shares one
  (Go, Gradle/Maven and Cargo do; **npm does not** — 300 MB+ per worktree). Sharing a
  `node_modules` between worktrees has its own hazards: `/usr/local/share/agent-fleet/notes/worktrees.md`.

## What is not available (check before you plan around it)

- **No root, no `sudo`** — you are `dev` (uid 1000); `apt install` is not possible. Install into
  your home instead (`~/.local`, `pip install --user`, `uv tool install`, `npm i -g` through the
  home-volume node). Anything that must be in the image is a request to the operator.
- **No Docker / Podman**, and no database servers (`psql`, `sqlite3`, `redis-cli` absent).
  Testcontainers, `docker compose` fixtures and "just start a Postgres" do not work — run such
  tests against a service the user provides, or skip them and say plainly that you did.
- `ss`, `lsof`, `netstat` are **not installed**; probe a port with
  `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:<port>/`.
- Present and usable: `gcc`/`g++`/`make`/`pkg-config` (so cgo, node-gyp and source-built wheels
  compile), `git`, `gh` (pre-authenticated through a wrapper — never run `gh auth login`), `go`,
  `python3` + `uv`, node via nvm, `chromium`, `rg`, `fd`, `jq`.
- Outbound network may be restricted; an unreachable host is not necessarily an error. Do not try
  to reach other tenants' or users' data — containers are isolated.

## Answering questions about this Workspace / environment

The user guide is at `/usr/local/share/agent-fleet/docs` — answer from what is there. (Usually
bind-mounted read-only by the Control Plane; where it can't mount, the Agent downloads the same
tree at start, so on a fresh container the directory may fill a moment after boot.) When asked how
this environment behaves (persistence, "recreate" vs "clean home" vs Stop→Start, build/memory
limits, gh transparent auth, connections, previews, MCP, toolchains, …), grep that tree and cite
the file rather than answering from memory — specs drift:
`grep -rni "<topic>" /usr/local/share/agent-fleet/docs`. If the directory is absent **or empty**,
answer from the policy's highlights and say the docs aren't available here.

**Every container gets the same tree** — it is not cut by role (ADR 0064), so you can rely on all
of it being there:

| Shelf | Reader |
|---|---|
| `member/` | someone running agents from the Console — start here for "how do I…" |
| `admin/` | a tenant administrator: members, limits, the audit log |
| `operate/` | whoever installs and keeps a deployment alive (host-level; needs shell access) |
| `ref/` | the capability tables everyone consults, and the glossary |
| `operate/runbooks/` | the command procedures, copied in from `deploy/` at release time |

**Match the shelf to who is asking.** A member's "how do I…" is answered from `member/`; do not
hand them a host-level procedure out of `operate/` — inside a container there is no root and no
Docker, so those steps are not theirs to run.

**The developer documentation is not here and never will be.** Architecture, the decision records
and the frozen work journals live in the repository's `docs/` tree, which ships to nobody. If a
question genuinely needs it, say it is in the developer documentation rather than inventing a path.
