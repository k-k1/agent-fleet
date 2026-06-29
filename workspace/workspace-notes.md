# Workspace Guide (operating policy)

This file is installed automatically into every agent-fleet Workspace container, and
every Claude / Codex / OpenCode session reads it at startup. Edit it in the repo at
`workspace/workspace-notes.md`; changes take effect after the image is rebuilt.

## About this environment
- This is your own per-user container. You drive several sessions from the browser Console.
- Working directories live under `~/repos/<repo>`. Clone repositories from the Console.
- The container can be recreated from "Settings > Environment" (rebuilt from the latest image).

## Do not
- **Do not leave uncommitted changes.** Recreating the container deletes cloned repos — commit / push often.
- **Do not store credentials in plaintext.** Never write API keys or tokens into repos or files. Manage connections under "Settings > Connections" (stored encrypted).
- **Do not touch or read the agents' internal state.** `~/.config/agent-fleet`, `~/.claude`, `~/.codex`, and `~/.local/share/opencode` hold credentials and the encrypted store. Leave them alone.
- **Do not run host-wide destructive commands.** No runaway `rm -rf`, fork bombs, crypto mining, or port scanning.
- **Do not hog resources.** The host is shared and memory-constrained. Heavy builds and large parallelism can exhaust memory and disrupt the whole fleet.

## Build memory (important — this has caused real incidents)
JVM build tools are the main cause of out-of-memory trouble on the shared host.
- **Gradle:** a conservative `~/.gradle/gradle.properties` is seeded for you — capped heap, a short daemon idle-timeout, no parallelism, limited workers. Projects may override it in their own `gradle.properties`.
  - Do not raise `org.gradle.jvmargs` heap unless a build genuinely needs it.
  - When you finish building, stop lingering daemons: `./gradlew --stop`.
  - If memory is tight, build with `./gradlew --no-daemon`, and avoid `--parallel` / a large `--max-workers`.
- **Maven and other JVM tools:** keep heaps small (e.g. `MAVEN_OPTS=-Xmx768m`) and do not leave watchers or daemons running.
- For long-running servers, open the port via the WS bar "Preview" instead of leaving ad-hoc processes up.

## Also
- Outbound network may be restricted; an unreachable host is not necessarily an error.
- Do not try to reach other tenants' or users' data. Containers are isolated.
