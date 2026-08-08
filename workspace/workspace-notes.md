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
  `~/.gradle`, the package caches (`~/.npm`, `~/go/pkg/mod`, `~/.cache/go-build`, `~/.cache/uv`,
  `~/.m2`, `~/.cargo`), and anything you put in `~` outside `~/repos`. Login and connections
  stay intact — and a *re-install* after a recreate is cheap because the caches are still warm.
- **The container filesystem outside home is ephemeral** — anything written under `/`, `/usr`,
  `/opt`, `/tmp` etc. reverts to the image on recreate. Persist tools in your home (e.g. under
  `~/.local`) if you want them to survive. (You cannot `apt install` in the first place — no
  root; see "What is not available".)
- So the only data loss risk from a recreate is `~/repos`: **commit / push before recreating.**

**Claude's memory / config persists too, and nothing deletes it.** Claude state
(`CLAUDE_CONFIG_DIR=/var/lib/af/claude`, including its saved memory under
`.../projects/*/memory/`) lives on a *separate* dedicated mount, not in home. No
Workspace operation removes it — not Stop/Start (only the container is removed; data
stays), not "recreate" (touches only `~/repos`), not "clean home" (touches only home).
There is no product action that wipes this separate Claude state; the container can
go away and come back with Claude memory intact. (Only an operator deleting the host
data dir would remove it.) This does not change the recreate and clean-home deletion
rules for `~/repos` and the regular home volume described above.

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

## You share one container with your other sessions
Every session you run lives in the **same** container: one filesystem, one process table, one
network namespace. Anything outside your own working directory belongs to someone else.
- **Never kill by pattern.** `pkill -f node`, `pkill -f claude`, `killall java` take down other
  sessions' agents and CLIs. Kill only PIDs you started yourself (`ps -o pid,ppid,args -p <pid>`
  to confirm before you do).
- **Ports are shared.** A port already in use is usually another session's server, not a stale
  process of yours — pick a different one. Read back the port the server actually printed (Vite
  silently moves 5173 → 5174, and then your "open 5173" instruction is wrong). `ss`, `lsof` and
  `netstat` are **not installed**; probe with
  `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:<port>/`.
- **Other working copies may be other sessions' desks — including the parent clone.** A session
  usually gets its own `~/repos/<repo>@wip-<slug>`, but one can just as well be launched
  directly in `~/repos/<repo>`. Don't assume either way: `git worktree list` and
  `git -C ~/repos/<repo> status --porcelain` tell you. In a copy that is not yours, what is
  forbidden is changing **what it is** — `checkout`/`switch`/`branch -f`/`stash`, any merge that
  could conflict or add a merge commit, `git worktree remove`/`prune`, deleting it (worktree
  lifecycle, cleanup and the shelf are the Console's job).
- **Keeping the parent's current branch fresh is a different thing, and someone has to do it.**
  A new worktree is created in the parent with `git worktree add -b <new> <dir> <base>`, and
  `<base>` resolves against the parent's **local** refs — that path never fetches (only an
  existing-branch launch fetches and fast-forwards). A stale parent therefore forks every later
  session off an old base, silently. So once the base branch has moved, refresh it:
  `git -C ~/repos/<repo> pull --ff-only`, on a clean tree that already sits on that branch —
  the same thing the Console's Fast-Forward on the repo row does. Dirty, or on another branch?
  Stop there and tell the user; don't "fix" it by checking anything out.
- Worktrees share one object store, so `git fetch`, `git gc`, tag and branch writes are visible
  to everyone, and **a branch can be checked out in only one worktree at a time**. Which
  branches are taken is not fixed: the parent clone sits on whatever it was last left on, which
  is not necessarily the base branch. `git worktree list` tells you who holds what.
- **Integrate in the right direction and the question stops mattering**: bring the base *into*
  your worktree with `git fetch origin && git merge origin/<base>`. That reads the
  remote-tracking ref, so it works whether or not a local branch of that name is checked out
  anywhere. (The Console's worktree row also offers Fast-Forward for parent → worktree.) Even
  when the base branch happens to be free, don't check it out here — you would only be taking
  it from the next session that needs it, and your session belongs on its own branch.
- Moving a branch that another working copy holds is refused by git in every form — `checkout`,
  `branch -f`, `push . HEAD:<branch>`, `fetch origin <branch>:<branch>` all fail with "checked
  out at <path>" (measured). **Never use `--ignore-other-worktrees`**: it is the one thing that
  succeeds, and then two working copies share one branch ref and silently revert each other's
  commits.
- **To land your work, push your branch** and let the user merge it (PR, or from the Console).
  Merging into the parent clone locally means editing another session's checkout, and a merge
  conflict there leaves that session sitting in a broken tree it never asked for.
- **Your directory name is not your session name** — a worktree keeps the slug of whichever
  session created it. Use `$AF_SESSION_NAME`.

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
- **No system `gradle` / `mvn` is installed — use the project wrapper** (`./gradlew`, `./mvnw`). When a JDK is available the wrapper fetches the version a project pins. Do not `apt install gradle`/`maven` (it will not work and is the wrong version). A project that lacks a wrapper cannot be bootstrapped here (no system `gradle`/`mvn` to run `gradle wrapper`) — commit the wrapper upstream instead.
  - **JDKs come from two places; check what's actually present with `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk* 2>/dev/null`.**
    - `/usr/lib/jvm/` — JDKs the deployment provides (baked or bind-mounted). Present on
      most local deployments (Temurin 8/21/25), but **may be empty** (e.g. the ECS
      runtime mounts nothing here). Never assume it's populated — list it first.
    - `~/.local/share/agent-fleet/jvm/` — the per-user home volume, where JDKs you add
      persist across restarts. **If no JDK is present (or you need another major),
      install one:** `workspace-agent install-jdk 21` (any major; downloads the latest
      GA Temurin for this arch as `temurin-21-jdk-<arch>`). This works on every runtime,
      including ECS. Selecting a Java version in the Console does the same automatically
      on the next container start.
  - `java` is not on `PATH` and `JAVA_HOME` is unset by default; wrappers resolve their
    own version. To call `java`/`javac` directly, point `JAVA_HOME` at one of the dirs
    above, e.g. `JAVA_HOME=$(ls -d /usr/lib/jvm/temurin-21-jdk* ~/.local/share/agent-fleet/jvm/temurin-21-jdk* 2>/dev/null | head -1)`.
    Or select the version in the Console (**Settings > toolchains**) so `JAVA_HOME` is
    exported into every session for you.
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
- For long-running servers, open the port from the workspace action bar's "Preview" control instead of leaving ad-hoc processes up.

## Dependencies in a worktree (disk, not just memory)
N worktrees of one repo means N copies of every per-project dependency tree, unless the
ecosystem already shares one (Go, Gradle/Maven and Cargo do; **npm does not**).
- **Check the disk before a big install** — `df -h ~`. The volume is shared with everything
  else you do, and caches grow without bound (`~/.npm` and `~/.cache` reach tens of GB).
- **Node — the expensive one** (a `node_modules` is easily 300 MB+ per worktree). You may share
  the parent clone's tree by symlink when the lockfiles are identical, but **`npm ci` through
  that link empties the parent's `node_modules`** and breaks every other session using it, and
  `rm -rf node_modules/` (trailing slash) deletes through the link the same way. Remove the link
  with `rm -rf node_modules` — no trailing slash — before any install.
- Per-language rules (what is already shared, what to do per worktree, and the measurements
  behind them) are in `dev/93-worktree-dependencies.md` under the read-only docs mount — see
  "Answering questions about this Workspace / environment" below for the path.

## What is not available (check before you plan around it)
- **No root, no `sudo`** — you are `dev` (uid 1000). `apt install` is simply not possible.
  Install into your home instead (`~/.local`, `pip install --user`, `uv tool install`, `npm i -g`
  through the home-volume node); those persist. Anything that genuinely must be in the image is
  a request to the operator, not something you can work around.
- **No Docker / Podman**, and no database servers (`psql`, `sqlite3`, `redis-cli` are absent).
  Testcontainers, `docker compose` fixtures and "just start a Postgres" do not work here. Run
  such tests against a service the user provides, or skip them — and say plainly that you did.
- Present and usable: `gcc`/`g++`/`make`/`pkg-config` (so cgo, node-gyp and source-built wheels
  compile), `git`, `gh` (already authenticated — see below), `go`, `python3` + `uv`, node via
  nvm, `chromium`, `rg`, `fd`, `jq`.

## Headless browser (UI verification / screenshots)
The fixed-version `chromium` binary, its runtime libraries, and fonts (DejaVu + Noto
CJK — Japanese renders correctly) are baked into the image. Use `chromium --headless`
directly, or point a browser automation library at `/usr/bin/chromium`; no per-user
browser download is required. Committed E2E belongs in `console-e2e/`
(`@playwright/test`), not `console/`.
- Run headless and short-lived; close the browser when done (memory-constrained host).
  Screenshots and WebGL (SwiftShader) work; there is no display for headful runs.
- **Headless reports a coarse pointer**: `(hover: hover)` and `(pointer: fine)` are false by
  default, so desktop hover styles never apply and you can "verify" the touch layout by
  accident. Force desktop input when that matters:
  `--blink-settings=primaryHoverType=2,availableHoverTypes=2,primaryPointerType=4,availablePointerTypes=4`
- dbus / GPU errors on stderr are normal noise. Judge success by the exit status and the file
  that was written, not by a clean stderr.

## Handing an owner-controlled Chromium page to the user
When automation inside the container needs a human to inspect or operate its existing
Chromium Page:

1. Start Chromium with `--remote-debugging-address=127.0.0.1`,
   **`--remote-debugging-port=0`** (never a fixed port — see below) and a non-default
   `--user-data-dir`. Never expose remote debugging on `0.0.0.0`. Then read
   `<user-data-dir>/DevToolsActivePort`: line 1 is the port Chromium actually took,
   line 2 is `/devtools/browser/<GUID>` — the identity of *your* browser.
   **Why not a fixed port:** your sessions share one container and one loopback. If
   another session already holds that port, your Chromium does **not** fail — it
   silently binds the IPv6 loopback and keeps running, while `127.0.0.1:<port>` stays
   the *other* session's browser. You would then list and attach to someone else's
   possibly logged-in page. (Measured; other users' containers stay unreachable.)
2. Call the Agent Fleet MCP tool `list_chromium_targets` with the port from
   `DevToolsActivePort` and choose the intended Page from its returned `target_id`; do
   not guess a target. Check the returned `browser_id` equals line 2's GUID; pass that
   GUID as `expected_browser_id` on attach so a collision is refused rather than
   silently mis-attached.
3. Before switching to `user-control`, stop the owner's automation against that Page.
   Chromium does not arbitrate competing owner and human input.
4. Call `attach_chromium`. **The attachment starts in `view-only`, and in that mode the
   agent rejects every pointer, wheel and key message the pane sends** — the user sees a
   live picture in which nothing they do has any effect, with no error. So if the user is
   meant to *operate* the page (not just read it), you must also move it to `user-control`
   — `request_browser_action` when they need instructions or completion/cancel controls,
   otherwise `set_chromium_control_mode`. Handing over only the link leaves them stuck.
5. Present the returned `open_url` unchanged as a Markdown link labelled
   "Open the browser and operate it". An MCP call alone must not change the user's
   Console layout; opening the link is the user's explicit action. The link opens the
   page as a **pane in the user's current tab**, not a new tab — say so rather than
   telling them to look for a new window.
6. Never perform a final publish, send, consent, or confirmation click for the user.
   An attachment or a user-reported completion is not proof that the external site's
   operation succeeded.
7. Check `get_browser_action_result` only when needed; do not poll it indefinitely.
   Once the user completes or cancels, use `set_chromium_control_mode` to lock the
   attachment if appropriate and call `detach_chromium`.

`detach_chromium` ends only Agent Fleet's connection and screencast. It must not close
the owner Page, BrowserContext, profile, or Chromium process. Never put a raw CDP
endpoint, cookie, password, or token in an answer, log, or commit.

## Browser pane (how the USER views a web app you run)
The Console has a **browser pane** that renders a web app running inside this
Workspace. It is a **user-facing Console feature** — the human opens it and looks at
it. **You do not have a tool to open, drive, or see the browser pane.** Do not act as
if you can see what it shows, and do not claim a page "looks right" based on it.

What you *can* do, and how to hand off to the user:
- Run the web app so it listens on `http://127.0.0.1:<port>` (loopback only — the pane
  connects there; external hosts are not shown).
- Tell the user the exact **port and path** to open (e.g. `127.0.0.1:5173` `/`), and
  point them at the flow: **Preview (プレビュー) → "Open in pane" (ペインで開く)**.
- Prefer the browser pane for anything live or interactive — **Vite HMR, WebSocket,
  SSE, Spring Boot**, or any app that pushes updates. For a plain static HTTP page the
  **lightweight preview** (軽量プレビュー, opens `/preview/{port}` in a new tab) is enough.
- Pane limits the user works within: at most **2 pages**, viewport ≤ **1600×1200**,
  ~**12 fps**. `target-unreachable` means the port is not listening yet (start the
  server, then Reload); `crashed` / `disconnected` mean the in-container Chromium died
  or the socket dropped — the user reconnects from the toolbar.
- The **smartphone layout does not expose this flow yet** — desktop and tablet are
  supported, but on a phone the user cannot open the pane, so don't tell them to.

Verification honesty:
- The pane is the *user's* view, separate from **your own headless Chromium** (see
  above). Only say you "verified" / 「確認しました」 a UI when **you** drove it with your
  own Chromium and observed the result — never on the basis of a pane you cannot see.
- Don't leave the dev server running once you're done — stop it (memory-constrained host).
- Never copy secrets that surface in the app — API keys, cookies, Console/devtools log
  contents — into logs, commits, or docs.

## Agent Fleet sessions (do not assume a terminal)
- A **session is a logical task/conversation**, not necessarily a tmux session or a
  dedicated CLI process. Codex and OpenCode normally run through a shared managed
  runtime and have no terminal pane; Claude, shell/SSM, and explicitly selected
  terminal-mode sessions use a terminal.
- Match the Console labels when explaining this to a user: **execution method**
  (`実行方式`), **Managed** (`マネージド`), and **Terminal (CLI)**
  (`ターミナル（CLI）`). `driver`, `runtime`, `TUI`, `PTY`, pane, and `tmux` are
  implementation terms; explain them only when useful for debugging or when the user
  explicitly asks how the system is built. TUI means the CLI's terminal interface;
  tmux keeps that interface alive behind Terminal (CLI).
- Do not advise a user to inspect, attach to, or kill tmux as the normal way to manage
  Agent Fleet sessions. Use the Console or the `af_*` session tools. Stopping,
  resuming, archiving, forking, and switching execution method are logical session
  operations and are routed to the correct backend automatically.
- A managed session can continue the same conversation after Agent/runtime restart
  through reconciliation, and Codex/OpenCode can switch execution method while idle.
  Do not infer that a missing terminal means the session stopped or lost its history.

## Talking to Agent Fleet from inside a session (the af MCP tools)
Your own session name is in `$AF_SESSION_NAME` — the same value an injected instruction carries
in its `[agent-fleet]` note. Don't infer it from a directory name.
- **`af_report(session=…)`** — call it once, when an instruction that carried the
  `[agent-fleet]` note is fully done and nothing is left. Not when you stop to ask a question,
  not when work continues. Forgetting it is harmless (completion is detected anyway); reporting
  early is not, so when in doubt don't call it.
- **`propose_session_handoff(title, prompt)`** — when your context is nearly spent, or the work
  splits cleanly, hand the next session a prompt it can execute as-is: what is unfinished, what
  you changed, the exact next steps. It **starts nothing** — the user reviews and edits it in
  the Console and picks the agent and model. You cannot create, stop or message other sessions;
  this proposal is the handoff channel. Commit and push before proposing one: the next session
  may run in a different worktree.
  **If the user directly asks you to hand off / continue elsewhere — "引き継いで", "hand this
  off", "continue in a new session" — that request itself is the trigger: call this tool right
  away.** There is no other way for you to start a handoff; do not substitute a plain-text
  summary or a to-do list in chat for the actual tool call.
- **Chromium attach tools** — see the section above.
- **Adding an MCP server is a Console action** (Settings → MCP), not a config edit. Agent Fleet
  owns its entries in `~/.claude.json`, `~/.codex/config.toml`, opencode's config and so on, and
  rewrites them; servers you hand-add there get wiped. The same goes for the policy files it
  seeds — `~/.codex/AGENTS.md` and `~/.config/opencode/AGENTS.md` are re-copied from the image
  at every container start. Durable project instructions belong in the repository's own
  `AGENTS.md` / `CLAUDE.md`; durable *environment* instructions are this file, which lives in
  the agent-fleet repo at `workspace/workspace-notes.md`.

## Command-environment quirks that have burned sessions
- **Don't assume the shell keeps its directory.** Agent harnesses reset the working directory
  between tool calls, so `cd ../other-worktree && …` in one call followed by `git commit` in the
  next commits to the wrong branch. Use absolute paths and `git -C <dir> …`.
- **`GIT_EDITOR=true` and `GIT_TERMINAL_PROMPT=0` are set for you.** Always pass `-m`/`-F` (an
  editor-driven commit would silently take an empty message), and expect credential failures to
  fail fast rather than hang. Genuinely interactive commands (`gh auth login`, `rebase -i`) must
  be run by the user in their own shell session.
- **`gh` is pre-authenticated** through a wrapper that injects the token from the user's saved
  connection on every call — use `gh` freely (CI runs, PRs, issues) and never run `gh auth
  login`. Some other commands are shims too; when output looks impossible, check with
  `type <cmd>` / `command -v <cmd>` and re-run the real binary.
- **Never run `workspace-agent` bare "to see the usage"** — with no subcommand it *starts a
  second Agent process* and touches live state. Only documented subcommands (`install-jdk`, …).
- **`env` contains live secrets** (`AF_*` tokens and keys). Never paste its output into a
  commit, a log, an issue, or an answer.
- Long builds outrun tool-call timeouts. Run them in the background and poll, instead of
  re-running a ten-minute build that looked "hung".
- The clock is the workspace's local timezone (`date`), not UTC.

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
