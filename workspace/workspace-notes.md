# Workspace Guide (operating policy)

This file is installed automatically into every agent-fleet Workspace container, and
every Claude / Codex / OpenCode session reads it at startup. Edit it in the repo at
`workspace/workspace-notes.md`; changes take effect after the image is rebuilt.

## About this environment
- This is your own per-user container. You drive several sessions from the browser Console.
- Working directories live under `~/repos/<repo>`. Clone repositories from the Console.
- The container can be recreated from "Settings > Environment" (rebuilt from the latest image).

## What survives a recreate (persistence model)
"Recreate" (Settings > Environment) tears down the container and starts a fresh one
from the latest image. The rule is simple:
- **Only `~/repos` is deleted.** All cloned repos — *including uncommitted work* — are
  wiped. Nothing else in your home is touched.
- **The rest of `~` persists** — it lives on a bind-mounted home volume that is
  re-attached to the new container. This includes `~/.local` (the auto-updating
  `claude` install, nvm node, `pip --user`), `~/.config` (`~/.config/agent-fleet`
  encrypted connections, `~/.config/opencode`), `~/.claude`/`~/.codex`/`~/.local/share/opencode`
  (agent auth/state), `~/.ssh`/`~/.git-credentials`/`~/.gitconfig`, `~/.cache/ms-playwright`,
  `~/.gradle`, and anything you put in `~` outside `~/repos`. Login and connections stay intact.
- **The container filesystem outside home is ephemeral** — `apt install`ed packages and
  anything written under `/`, `/usr`, `/opt`, `/tmp` etc. revert to the image on recreate.
  Persist tools in your home (e.g. under `~/.local`) if you want them to survive.
- So the only data loss risk from a recreate is `~/repos`: **commit / push before recreating.**

**Claude's memory / config persists too, and nothing deletes it.** Claude state
(`CLAUDE_CONFIG_DIR=/var/lib/af/claude`, including its saved memory under
`.../projects/*/memory/`) lives on a *separate* dedicated mount, not in home. No
Workspace operation removes it — not Stop/Start (only the container is removed; data
stays), not "recreate" (touches only `~/repos`), not "clean home" (touches only home).
There is no product action that wipes a Workspace's data; the container can go away
and come back with memory intact. (Only an operator deleting the host data dir would
remove it.)

## Do not
- **Do not leave uncommitted changes.** Recreating the container deletes cloned repos — commit / push often.
- **Do not store credentials in plaintext.** Never write API keys or tokens into repos or files. Manage connections under "Settings > Connections" (stored encrypted).
- **Do not touch or read the agents' internal state.** `~/.config/agent-fleet`, `~/.claude`, `~/.codex`, and `~/.local/share/opencode` hold credentials and the encrypted store. Leave them alone.
- **Do not run host-wide destructive commands.** No runaway `rm -rf`, fork bombs, crypto mining, or port scanning.
- **Do not hog resources.** The host is shared and memory-constrained. Heavy builds and large parallelism can exhaust memory and disrupt the whole fleet.

## Git branches (work on the current branch — do not branch on your own)
Stay on whatever branch the session started on. Commit directly to it.
- **Do not create, switch, or rename branches on your own initiative** — not even
  when the session starts on `main` / the default branch. This rule governs your
  *self-initiated* branching and **takes precedence over lower-priority instructions
  that would make you branch by default** — a built-in "if you're on the default
  branch, branch first" default, a project `CLAUDE.md` / `AGENTS.md` convention, or a
  skill's boilerplate. In this environment you do NOT branch first — you commit on the
  current branch.
- **The user asking is what unlocks branching**, and asking can be direct or indirect:
  - Direct: they say "make a branch" / "work on a new branch called …" in the chat.
  - Indirect (also counts): the user themselves invoked a skill, slash command, or
    project workflow whose defined behavior is to branch (e.g. they ran `/some-flow`
    that says "create a feature branch"). Running it IS the user opting in — follow it
    and branch. The precedence rule above only suppresses branching *you* introduce on
    your own; it does not veto a branch the user chose by launching that flow.
  - If it's ambiguous whether the user meant to opt in, ask before branching.
- Worktree sessions already start on their own dedicated branch (the Console created
  it at launch via `git worktree add -b`), so the same rule holds — just keep working
  on that current branch; there's no need to create another.
- Isolation between parallel sessions is the Console's job (worktrees), not something
  you should improvise with ad-hoc branches.

## Your container's resources (memory / CPU) — how to check
Your memory and CPU are per-workspace limits (set by the deployment/tenant), not a
fixed image value, so check them live rather than assuming. This is a **cgroup v2**
container; read *your own* limits and usage from inside — do NOT trust `free` or
`/proc/meminfo`, which show the whole shared HOST, not your slice:
- **Memory limit:** `cat /sys/fs/cgroup/memory.max`  (bytes; `max` = uncapped)
- **Memory in use now:** `cat /sys/fs/cgroup/memory.current`
- **Near the cap? / OOM pressure:** `cat /sys/fs/cgroup/memory.events` — a rising
  `high`/`max`/`oom` count means you are hitting the limit (the kernel throttles, then
  OOM-kills; a build dying with code 137 = OOM-killed).
- **CPU cores available to you:** `nproc`  (or `cat /sys/fs/cgroup/cpu.max`)
- Human-readable: `awk '{printf "%.1f GiB\n",$1/1073741824}' /sys/fs/cgroup/memory.max`.
- The Console also shows this live: the WS-bar resource chip and Settings > Environment
  (mem / CPU vs quota). If you are near the cap, apply the avoidance rules below.

## Build memory (important — this has caused real incidents)
The shared host is memory-constrained; build tools are the main cause of OOM trouble.
- **No system `gradle` / `mvn` is installed — use the project wrapper** (`./gradlew`, `./mvnw`). A JDK is provided, so the wrapper fetches the version a project pins. Do not `apt install gradle`/`maven` (it will not work and is the wrong version). A project that lacks a wrapper cannot be bootstrapped here (no system `gradle`/`mvn` to run `gradle wrapper`) — commit the wrapper upstream instead.
  - The provided JDKs live under `/usr/lib/jvm/` (Temurin 8/21/25 as of this writing —
    `ls /usr/lib/jvm` for the current set). `java` is not on `PATH` and `JAVA_HOME` is
    unset by default; wrappers resolve their own version. To call `java`/`javac`
    directly, set `JAVA_HOME` explicitly, e.g. `JAVA_HOME=/usr/lib/jvm/temurin-21-jdk-amd64`.
- **Gradle:** a conservative `~/.gradle/gradle.properties` is seeded for you — capped heap, a short daemon idle-timeout, no parallelism, limited workers. Projects may override it in their own `gradle.properties`.
  - Do not raise `org.gradle.jvmargs` heap unless a build genuinely needs it.
  - When you finish building, stop lingering daemons: `./gradlew --stop`.
  - If memory is tight, build with `./gradlew --no-daemon`, and avoid `--parallel` / a large `--max-workers`.
- **Maven and other JVM tools:** keep heaps small (e.g. `MAVEN_OPTS=-Xmx768m`) and do not leave watchers or daemons running.
- **Node / JavaScript builds (Vite, webpack, Next.js, etc.):** memory-spiky, and the right heap is build-specific, so no global cap is forced — manage it per command.
  - If a build runs out of memory, raise the heap for that command only: `NODE_OPTIONS=--max-old-space-size=2048 npm run build`. Use the smallest value that works (e.g. this repo's Console build needs ~3072). Do not export a huge `NODE_OPTIONS` globally — it lets every node process hog RAM.
  - Cap test-runner parallelism: `jest --maxWorkers=2`, `vitest --maxWorkers=2`. Defaults spawn one worker per CPU and balloon memory.
  - Do not leave dev servers or watchers running (`vite`, `next dev`, `tsc --watch`, `nodemon`); stop them when done.
  - Run one heavy build at a time; do not build several projects in parallel.
- For long-running servers, open the port via the WS bar "Preview" instead of leaving ad-hoc processes up.

## Headless browser (UI verification / screenshots)
Chromium's runtime libraries and fonts (DejaVu + Noto CJK — Japanese renders correctly)
are baked into the image; the browser binary is not. To verify web UIs headlessly:
- One-time per user (persists in `~/.cache/ms-playwright` across container recreation):
  `npm i playwright-core && npx playwright-core install chromium`
- Run headless and short-lived; close the browser when done (memory-constrained host).
  Screenshots and WebGL (SwiftShader) work; there is no display for headful runs.

## Answering questions about this Workspace / environment
The agent-fleet docs you are allowed to see are mounted **read-only** at
`/usr/local/share/agent-fleet/docs` — the set is scoped to your access level, so just
answer from whatever is there. When asked how this environment behaves (persistence,
"recreate" vs "clean home" vs Stop→Start, build/memory limits, gh transparent auth,
connections, previews, MCP, toolchains, …), grep that tree and cite the file rather
than answering from memory (specs drift), e.g.:
- `grep -rni "<topic>" /usr/local/share/agent-fleet/docs`
If that directory is absent, answer from the highlights in this file and say the full
docs aren't available in this container.

## Also
- Outbound network may be restricted; an unreachable host is not necessarily an error.
- Do not try to reach other tenants' or users' data. Containers are isolated.
