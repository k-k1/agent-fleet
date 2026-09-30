---
audience: "anyone dealing with worktree dependencies and build caches"
source_of_truth: "the code named in each section; measurements inside a Workspace, each labelled with when and where it was taken"
updated: "2026-09"
---

# 93. Worktree dependencies and build caches, measured per ecosystem

English | [日本語](93-worktree-deps.ja.md)

A session usually runs in its own worktree, so many worktrees of one repository can exist
at once. What that costs is **not memory but disk, and whether a thing is shared** — and
the answer differs per ecosystem. This chapter holds the mechanism and the evidence. The
rules themselves live elsewhere, each with its own reader:

- **Every agent in every Workspace** starts with the operating policy
  ([workspace-notes.md](../../workspace/workspace-notes.md)) loaded, and reads the topic
  file for its situation when it needs it:
  [notes/worktrees.md](../../workspace/notes/worktrees.md) ("Dependencies in a worktree")
  and [notes/environment.md](../../workspace/notes/environment.md) (`/scratch`, disk,
  reclaiming cache space). These ship inside the image, where `docs/` does not, so they
  cannot link here and carry their own short version.
- **This repository's `console/`** has its own recipe in [AGENTS.md](../../AGENTS.md)
  ("`console/node_modules` in a worktree").
- **Why the `ecs` runtime moves build output onto a task-local disk**, with the EFS
  measurements behind it, is [ADR 0044](../decisions/0044-workspace-sizing.md), decisions
  3 and 5.

Two persistence facts drive everything:

- **A recreate deletes the working copies** (`~/repos`) — and with them every worktree's
  dependency tree.
- **The package caches in the home survive a recreate** (`~/.npm`, `~/go/pkg/mod`,
  `~/.cache/go-build`, `~/.cache/uv`, `~/.gradle`, `~/.m2`, `~/.cargo`). One exception,
  in 93.1: on the `ecs` runtime some of them live on a disk that is emptied on every stop.

So **reinstalling is cheap and duplicating is expensive.** What to cut is *the Nth copy
of the same thing*, never the cache.

## 93.1 What the Workspace itself does to a new working copy

Nothing links a dependency tree between worktrees on its own; sharing `node_modules` is a
manual step (93.3). What does happen automatically:

| Mechanism | Where it runs | What it does | Code |
|---|---|---|---|
| Submodule seeding | every runtime, on `git worktree add` | best effort: a submodule the parent clone has in its own store, and whose path in the worktree is still empty, is cloned from that store (hardlinked); anything skipped or failed is left to the normal update that follows. The measurements are in [build/04](04-agent.md) §4.6 | `finishNewWorktree` → `seedSubmodulesFromParent` (`workspace/agent/internal/gitx/git.go`, `git_submodule_seed.go`) |
| Build-output relocation | only where `$AF_WS_SCRATCH` is set, on a new clone or a new worktree (never on a relaunch into an existing one) | runs `af-scratch --auto`, which tries to make the build-output directory next to each marker file a symlink into `/scratch`, ideally while it is still empty; best effort (errors are logged and swallowed) | `scratchAutoRelocate` (`workspace/agent/scratch.go`), `workspace/af-scratch.sh` |
| Home-cache relocation | only where `$AF_WS_SCRATCH` is set **and** `/scratch` is at least `AF_WS_SCRATCH_MIN_GB` (30 GiB), at container start | tries to replace `~/.cache/go-build`, `~/.cache/uv` and `~/go/pkg/mod` (the default of `AF_WS_SCRATCH_DIRS`) with symlinks into `/scratch/home`; skipped when `/scratch/home` is not writable, and per directory when a move fails | `workspace/entrypoint.sh` |

**Who gets `$AF_WS_SCRATCH`.** The control plane sets it in the `ecs` adapter only
(`registerTaskDef` in `control-plane/internal/runtime/runtime_ecs.go`). The `ecs-ec2`
adapter leaves it out on purpose — home is already on local EBS there
(`runtime_ecs_ec2.go`, the note citing ADR 0045 decision 10-3) — and the docker and native
adapters do not set it. The workspace image creates a `/scratch` directory
(`workspace/Dockerfile`), so it exists on docker and `ecs-ec2` too; the traditional native
mode runs a host-built agent without the image. So the directory's existence says
nothing: the two relocation rows key on the variable (submodule seeding does not depend on
it). Where the variable is set, `/scratch` is task-local and emptied when the task stops.

**The markers `af-scratch --auto` looks for** (searched to depth 3,
`AF_WS_SCRATCH_AUTO_DEPTH`): `package.json` → `node_modules`; `Cargo.toml` or `pom.xml` →
`target`; `pyproject.toml` → `.venv`; `build.gradle` / `build.gradle.kts` → `build`. The
`case` in `af-scratch.sh` holds the real list. An existing symlink is left alone, and an
existing directory is moved only if `git check-ignore` says it is ignored.
`AF_WS_SCRATCH_AUTO=0` turns the whole step off.

**The working disk's size** decides whether the home caches move. The `ecs` adapter's
deployment default is 50 GiB (`ecsDefaultWorkDiskGiB`, overridden by `AF_ECS_WS_DISK_GB`),
which is above the 30 GiB threshold. A stack created before that default keeps the value
it was created with (ADR 0044 decision 5), and `AF_ECS_WS_DISK_GB=0` goes back to
Fargate's free 20 GiB — below the threshold, so the caches stay on EFS while the build
output still moves.

## 93.2 At a glance

The `/scratch` column is what 93.1 *attempts* on `ecs`: build output only next to a marker
file it found (and not with `AF_WS_SCRATCH_AUTO=0`), home caches only on a disk of 30 GiB
or more. It is not a guarantee for every project.

| Ecosystem | Shared by default | Grows per worktree | On `ecs`, under `/scratch` | What to do |
|---|---|---|---|---|
| Node (npm) | `~/.npm` (the tarball cache only) | `node_modules`, hundreds of MB (93.3) | `node_modules` | symlink to the parent clone **only when the lockfiles match**; otherwise `npm ci --prefer-offline` from the warm cache |
| Go | `~/go/pkg/mod` and `~/.cache/go-build` | effectively nothing | both caches, when the disk is ≥ 30 GiB | nothing. **The pressure is memory** — cap the test parallelism |
| Python | `~/.cache/uv`; `~/.cache/pip` (downloads only — a bare `pip install` lands in the shared `~/.local`, 93.5) | `.venv`, tens to hundreds of MB (estimate) | `~/.cache/uv` when the disk is ≥ 30 GiB; `.venv` when there is a `pyproject.toml` | one `.venv` per worktree with `uv` |
| JVM | `~/.gradle` and `~/.m2` | `build/` or `target/` | `build/` or `target/` | nothing — just be careful how you stop the daemon |
| Rust | `~/.cargo` (registry) | `target/`, gigabytes (estimate) | `target/` | keep it per worktree; a shared target directory serialises parallel builds (93.7) |

Check the disk with `df -h ~` (and `df -h /scratch` where `$AF_WS_SCRATCH` is set). Reclaiming cache
space — `npm cache clean --force`, `uv cache prune`, `go clean -cache`, or Settings >
Machine > Tool caches — is covered in notes/environment.md ("Disk"). These caches are
**shared by every worktree**, so do not clear them while another session is building.

## 93.3 Node — the only one that loses unless you share explicitly

`node_modules` is duplicated whole per worktree. **Measured:** this repository's
`console/node_modules` in the parent clone was 559 MB on disk (`du -sh`; 494 MB apparent
size) and 20,719 files on 2026-09-29, in a Workspace without `$AF_WS_SCRATCH`. The 2026-08
measurement was 349 MB; the tree grows with the dependencies, so read either number as a
snapshot.

**The parent clone's tree can be shared by symlink**, with one condition: **the lockfile
is identical to the parent's** (`cmp -s` the two, then link `node_modules` to the parent's).
The commands are not repeated here: AGENTS.md has this repository's, and
notes/worktrees.md gives every agent the general rule and its hazards. Both link with
`ln -sfT`, never a plain `ln -s`, for the reason below.

**Measured in 2026-08** in this repository's `console/` (npm 10.9.8, node 22.23.2,
Vite 7). `console/` has since moved to Vite 8 (`console/package.json`); this chapter did
not re-run these.

- The test runner works through the link in both vitest projects, and so does the
  production build. **Bundlers follow symlinks by default**, so resolution needs no help.
- ⚠️ One exception, and it needs a config line: a Vite `…?url` import resolves to the
  link's *target*, outside the root Vite allows by default, and is refused with
  `Error: Denied ID …`. `console/vite.config.js` (`afFsAllow`) adds `node_modules`' real
  path to `server.fs.allow`, which is a no-op for a real install.
- ⚠️ **Running `npm ci` through the link empties the parent's tree.** The link is
  replaced by a real directory and **every other session sharing it is left without
  one**. Remove the link before any install.
- ⚠️ **`rm -rf node_modules/` — with the trailing slash — deletes through the link the
  same way.** Without the slash, only the link goes.
- `npm install <pkg>` silently replaces the link with a real tree. Nothing breaks, but
  that worktree no longer shares and carries its own copy.

**Where `af-scratch --auto` has already made `node_modules` a symlink** (a new clone or
worktree with `$AF_WS_SCRATCH` set, 93.1), a plain `ln -s` shares
nothing: `ln -s <target> node_modules` onto an existing symlink to a directory creates the
new link *inside* that directory and exits 0. **Measured** with plain directories on 2026-09-29
(GNU coreutils `ln`). Remove the pre-created link first (`rm -rf node_modules`, no
slash); `ln -sfT` replaces a symlink and refuses a real directory. If the parent's own
`node_modules` was relocated the same way, its target is on `/scratch`: after a stop the
links in `~/repos` remain but point at nothing. A parent cloned without the relocation
(before it existed, or with `AF_WS_SCRATCH_AUTO=0`) keeps its tree in the home.

When the lockfiles differ, do not share — `npm ci --prefer-offline` installs from the
warm `~/.npm`. pnpm is not installed; Node 22 ships `corepack`, so a project that uses
pnpm can have its content-addressable store, which does not have this problem at all.

## 93.4 Go — do nothing; the pressure is memory

The module cache (`~/go/pkg/mod`) and the build cache (`~/.cache/go-build`) are
per-user, not per-worktree (`go env GOMODCACHE GOCACHE`, checked 2026-09-29), so an
extra worktree costs essentially nothing. What runs out instead is memory:
`go test ./...` compiles and runs **package-by-package in parallel**. On a busy host,
cap it with `-p 2`. This repository's own commands are in AGENTS.md ("Running the Go
tests").

`GOTOOLCHAIN` is `auto` (the Go distribution's `go.env`), so a module that pins a newer
Go downloads that toolchain into the module cache. In the home that survives a recreate
and is paid once. On `ecs` with a working disk of 30 GiB or more, the module cache and
the build cache are on `/scratch` (93.1), so both — and any downloaded toolchain — are
rebuilt after every stop.

## 93.5 Python — the default `pip` is the dangerous one

The image writes `break-system-packages = true` to `/etc/pip.conf`
(`workspace/Dockerfile`), so the PEP 668 marker on Debian's Python does not stop a bare
`pip install`. Run as `dev`, it **does not error** — pip falls back to a user install in
`~/.local`. That location **persists and is shared by every project**, so it breaks
quietly the moment two worktrees need different versions. (pip's download cache,
`~/.cache/pip`, is shared too, and harmlessly — it is one of the caches Settings > Machine >
Tool caches lists, `workspace/agent/tool_caches.go`.)

The right answer is a virtual environment per worktree, with `uv`, which the image
installs:

```bash
uv venv && uv pip install -r requirements.txt
```

uv's documented default on Linux is to hardlink packages from its cache, so a second
worktree costs little disk — when the cache and the `.venv` are on the same file system;
across file systems uv falls back to copying. That is uv's documentation, not a
measurement here. It matters on `ecs`: with a working disk under 30 GiB the uv cache
stays in the home while a relocated `.venv` is on `/scratch`. Note also that
`af-scratch --auto` pre-creates `.venv` only for a `pyproject.toml`; a project with only
a `requirements.txt` needs `af-scratch .venv` by hand.

**Never copy or symlink a virtual environment between worktrees** — it has absolute paths
baked in.

## 93.6 JVM — already shared; just mind how you stop it

`~/.gradle` and `~/.m2` are shared across worktrees already; only the build output
(`build/`, `target/`) is per worktree. No JDK is baked into the image; JDK discovery and
the heap and daemon rules belong to [notes/build.md](../../workspace/notes/build.md). The
entrypoint seeds `~/.gradle/gradle.properties` when it is missing, with a two-minute
daemon idle timeout among other limits (`workspace/entrypoint.sh`).

One worktree-specific hazard: **`./gradlew --stop` stops every daemon of that Gradle
version for the user**, and every session in the container is the same user with the same
`~/.gradle` — so running it while another session is building takes theirs down too
(Gradle's documented behaviour, not measured here). Doing it when you finish is fine;
doing it reflexively "because it is heavy" is not.

## 93.7 Rust and the languages that are not in the image

The Dockerfile installs no Rust toolchain, so install `rustup` yourself; `~/.cargo`
persists in the home (it is not in the relocation list) and its registry cache is shared
automatically.

The target directory reaches gigabytes (estimate). Pointing several worktrees at one
shared target directory (`CARGO_TARGET_DIR`) saves that disk, but Cargo takes a build lock
on it, so parallel sessions **serialise, each waiting for the other's build** ("Blocking
waiting for file lock on build directory" — Cargo's behaviour, not measured here). With
sessions building at the same time, a target directory per worktree is the default to
prefer; a shared one trades that concurrency for disk.

The general shape: **the caches in the home are shared for free; sharing output in the
worktree is the exception**, done only where the tool tolerates it — Node's
`node_modules` under the lockfile condition of 93.3. And with no root available, choose installers that work in
user space — `rustup`, `uv tool install`, `npm i -g` through the home's Node.
