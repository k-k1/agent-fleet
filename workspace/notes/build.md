# Build memory and toolchains (JDK, Gradle, Maven, Node)

Read when: you are about to run a JVM or Node build or test suite, need a JDK / `JAVA_HOME`, or a
build died (exit 137 = OOM-killed). The shared host is memory-constrained and build tools are the
main cause of OOM trouble — this has caused real incidents. Your own limit:
`cat /sys/fs/cgroup/memory.max` (details in `/usr/local/share/agent-fleet/notes/environment.md`).

## JVM: use the project wrapper, find the JDK the right way

- **No system `gradle` / `mvn` — use the project wrapper** (`./gradlew`, `./mvnw`). It downloads
  the *Gradle / Maven* version the project pins, not a JDK. `apt install gradle`/`maven` is
  impossible here and would be the wrong version; a project without a wrapper cannot be
  bootstrapped (nothing to run `gradle wrapper` with) — commit the wrapper upstream instead.
- **JDKs come from two places, so list both before assuming:**
  `ls -d /usr/lib/jvm/temurin-*-jdk* ~/.local/share/agent-fleet/jvm/temurin-*-jdk* 2>/dev/null`.
  `/usr/lib/jvm` is deployment-provided (baked or bind-mounted; Temurin 8/21/25 on most local
  deployments, but **empty on ECS**), `~/.local/share/agent-fleet/jvm` is the per-user home
  volume that survives restarts and is the only source on ECS.
- **Nothing there, or you need another major?** `workspace-agent install-jdk 21` downloads the
  latest GA Temurin for this arch as `temurin-21-jdk-<arch>`, on every runtime. The user's
  terminal-free equivalent: **Settings > Toolchains** (「ツールチェーン」) — the picker lists
  installed versions plus 8/11/17/21/25, with an **Install** button for one that isn't on disk
  (background download; sessions started after it finishes get the new `JAVA_HOME`, no restart).
  A version selected but never installed is fetched at the next container start.
- **`JAVA_HOME` and `java` on `PATH` follow that selection** — with a version selected the
  entrypoint exports both for the agent and for every session/shell launched afterwards, and a
  changed selection applies at the next launch (no Stop → Start). With no selection — the default
  for a fresh workspace — both are absent; select a version instead of working around it. Check
  with `echo "$JAVA_HOME"` / `command -v java`.
- Setting `JAVA_HOME` by hand is the fallback, and **never as `ls … | head -1`**: both dirs use
  `temurin-<major>-jdk-<arch>` and `amd64` sorts before `arm64`, so a home volume filled on x86
  and later attached to an arm64 box makes the first match the one whose `bin/java` cannot exec.
  Pin your arch:
  `a=$(dpkg --print-architecture); JAVA_HOME=$(ls -d /usr/lib/jvm/temurin-21-jdk-$a ~/.local/share/agent-fleet/jvm/temurin-21-jdk-$a 2>/dev/null | head -1)`.

## Gradle / Maven

- A conservative `~/.gradle/gradle.properties` is seeded for you (capped heap, short daemon
  idle-timeout, no parallelism, limited workers; projects may override it in their own
  `gradle.properties`). Don't raise `org.gradle.jvmargs` unless a build genuinely needs it, stop
  lingering daemons with `./gradlew --stop`, and if memory is tight build with `--no-daemon` and
  avoid `--parallel` / a large `--max-workers`.
- **Maven and other JVM tools:** keep heaps small (`MAVEN_OPTS=-Xmx768m`) and leave no daemons.

## Node / JavaScript

Vite, webpack and Next.js builds are memory-spiky and the right heap is build-specific, so nothing
is capped globally — manage it per command:

- Out of memory? Raise the heap for that command only:
  `NODE_OPTIONS=--max-old-space-size=2048 npm run build`, smallest value that works. Never export
  a big `NODE_OPTIONS` globally.
- Cap test runners: `jest --maxWorkers=2`, `vitest --maxWorkers=2` (defaults spawn one per CPU).
- Don't leave dev servers or watchers running (`vite`, `next dev`, `tsc --watch`, `nodemon`), and
  run one heavy build at a time.
- Long builds outrun tool-call timeouts: run them in the background and poll instead of
  re-running a ten-minute build that looked "hung".
- For long-running servers, use the workspace action bar's "Preview" control rather than leaving
  ad-hoc processes up.
