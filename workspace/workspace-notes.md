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

## Do not
- **Do not leave uncommitted changes.** Recreating the container deletes cloned repos — commit / push often.
- **Do not store credentials in plaintext.** Never write API keys or tokens into repos or files. Manage connections under "Settings > Connections" (stored encrypted).
- **Do not touch or read the agents' internal state.** `~/.config/agent-fleet`, `~/.claude`, `~/.codex`, and `~/.local/share/opencode` hold credentials and the encrypted store. Leave them alone.
- **Do not run host-wide destructive commands.** No runaway `rm -rf`, fork bombs, crypto mining, or port scanning.
- **Do not hog resources.** The host is shared and memory-constrained. Heavy builds and large parallelism can exhaust memory and disrupt the whole fleet.

## Build memory (important — this has caused real incidents)
The shared host is memory-constrained; build tools are the main cause of OOM trouble.
- **No system `gradle` / `mvn` is installed — use the project wrapper** (`./gradlew`, `./mvnw`). A JDK is provided, so the wrapper fetches the version a project pins. Do not `apt install gradle`/`maven` (it will not work and is the wrong version). A project that lacks a wrapper cannot be bootstrapped here (no system `gradle`/`mvn` to run `gradle wrapper`) — commit the wrapper upstream instead.
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
When asked how this environment behaves (persistence, "recreate" vs "clean home" vs
Stop→Start, build/memory limits, gh transparent auth, connections, previews, MCP,
toolchains, …), the authoritative spec is the **agent-fleet repo's `docs/`** tree —
this notes file only carries the load-bearing highlights. If that repo is cloned
(commonly `~/repos/agent-fleet`), grep its docs before answering rather than guessing:
- `grep -rni "<topic>" ~/repos/agent-fleet/docs` — member-facing guides under
  `docs/guide/`, internals under `docs/dev/`, decisions under `docs/decisions/`.
- Cite the file you found the answer in, and prefer it over memory (specs drift).
If the repo is not cloned, say what this notes file covers and point the user at `docs/`.

## Also
- Outbound network may be restricted; an unreachable host is not necessarily an error.
- Do not try to reach other tenants' or users' data. Containers are isolated.
