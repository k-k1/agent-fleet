---
audience: "someone building this repository for the first time"
source_of_truth: "the code and the CI definitions"
updated: "2026-07"
---

# 10. Development — building, reflecting a change, testing, conventions

English | [日本語](10-development.ja.md)

## 10.1 Repository layout (responsibilities only)

| Directory | Responsibility |
|---|---|
| `console/` | The browser SPA (React + Vite + zustand). Its build output is served statically by the CP |
| `control-plane/` | The Control Plane (Go, its own module). Migrations are embedded and applied at start |
| `workspace/` | The workspace image (Dockerfile, entrypoint) plus `workspace/agent/`, the agent as a separate Go module |
| `deploy/` | The deployment layer (local / compose / aws). The runbooks are the READMEs there ([09](09-deploy.md)) |
| `e2e/` | Fleet end-to-end tests (a separate Go module, standard library only) — the CP against real containers (§10.4) |
| `console-e2e/` | Console UI end-to-end tests (Playwright) — browser through CP to a real container (§10.4) |

The file-level map is [90-code-map](90-code-map.md).

## 10.2 What to do to see a change

**The key fact: `docker run` is a no-op against an already-running container. A new
image only takes effect on Stop → Start** (Start removes and re-runs, so the swap is
guaranteed). The home directory — logins, connections, repositories — is a bind mount
and is unaffected by an image update.

| What you changed | What it takes |
|---|---|
| Console (`console/src`) | `vite build` (watch works) → **reload the browser**. The CP serves the bundle with no-store, so it does not need restarting |
| The CP's Go | rebuild and restart the CP (`restart-cp.sh`). No image rebuild |
| The agent's Go, or anything baked into the image | rebuild the image → each user does **Stop → Start** from the Console. The CP never force-swaps a running workspace |
| The pinned version of a baked CLI | follow the runbook in §10.2.1 |
| The pinned `rtk` version | same as the CLIs: an image ARG. Bump, rebuild, Stop → Start. Following latest instead is an opt-in self-update |
| Anything the entrypoint applies (seeded config, timezone…) | Stop → Start only; no rebuild |
| The shared JVM | delete the shared directory and re-provision it |

### 10.2.1 Bumping a baked tool — the standard procedure

Follow this exactly when raising the version of a baked CLI, `gh`, or Go. The reason
it is a procedure at all: an unpinned `npm install -g` hits the Docker layer cache, so
**rebuilding does not actually raise the version** — which is why every version is an
ARG now ([04 §4.9](04-agent.md)).

1. **Check what latest is.** For the CLIs, `npm view <package> version`. For `gh`, its
   releases page. For Go, keep step with the `go` directive in
   `workspace/agent/go.mod` — if you are not raising that, leave it.
2. **Bump the ARG** in `workspace/Dockerfile`. Changing an ARG reliably breaks the
   cache, so `--no-cache` is unnecessary.
3. **Commit and push** — a one-line diff, with a Japanese message.
4. **Wait for CI to go green.** The push fires the end-to-end workflow, which builds
   the image and verifies L1 (**the installed version equals the pin**), L2 (fleet
   connectivity) and L3 (Console UI). **Do not proceed while it is red.**
5. **(Larger bumps only) the jobs that use real credentials.** These consume different
   quotas per agent, so the workflows and their inputs are **separate per agent**. Run
   only the one you need; all default to off.
6. **Reflect it on the host** with `run-dev.sh`. The image smoke test runs right after
   the build and re-verifies the version match.
7. **Reflect it in each workspace**: every user does **Stop → Start** from the Console.
   Their home and repositories survive.
8. **(Optional) confirm** from inside the restarted container, or from the Console's
   toolchain settings, which show the effective, image and pinned versions side by side.

Two notes:

- **A weekly scheduled run** catches upstream CLI and base-image breakage even when
  nothing in this repository changed. If it goes red, suspect upstream and use steps
  4–5 to isolate.
- To move a single member forward without rebuilding, there is an opt-in self-update.
  Stop → Start returns them to the baked version.

## 10.3 What the start scripts do (`deploy/local/`)

- **`run-dev.sh`** — the **single entry point**, with subcommands. It prepares the
  workspace runtime, builds the Console, builds the CP, and starts the CP as a host
  process. It sources a git-ignored env file automatically, so authentication and
  crypto settings reach the CP; without it you get a plain dev start.

  | Subcommand | What it does |
  |---|---|
  | (none) / `local` | the development default, Docker runtime |
  | `wsl` | a WSL preset (Docker and cgroup preflight, `AUTH=dev` fixed) |
  | `native` | containerless, no Docker (single user — [ref/deploy-targets](../../guide/ref/deploy-targets.md)). Builds the agent on the host and hands it over |
  | `reset [--all] [--yes]` | wipe local data. Refuses while the CP is running, and cleans up the leftovers of both runtimes before deleting |

  ⚠️ The env value `AF_RUNTIME=wsl` is an alias for *containerless*, which is a
  different thing from the `wsl` subcommand (a Docker preset). Prefer the subcommand;
  the two are easy to confuse.

- **`restart-cp.sh`** — the light path: rebuild only the Console and the CP, swap the
  running CP process in place, and verify its health endpoint. **It does not rebuild
  the workspace image.** `SKIP_CONSOLE=1` limits it to the Go side.
- **`e2e-smoke.sh`** — the image smoke test (L1): does the CLI version actually
  installed in the built image match the Dockerfile's pin (that is, is the cache
  stale), and is everything that should be baked in present. It runs automatically
  after a build.

Host-specific practice (PATH, docker group membership and so on) belongs to the
handoff notes, not here.

## 10.4 Testing

Go is **two modules**, run separately:

```bash
(cd control-plane   && go test ./...)
(cd workspace/agent && go test ./...)
```

- The CP side carries many `httptest`-based smoke tests. The Postgres ones skip
  themselves unless a database URL is set.
- ⚠️ **When you add a migration, run it once against a real Postgres.** Three tests
  skip without a database, and they are **the only place** that catches "added to one
  dialect but not the other" ([06 §6.4](06-data.md)). Standing one up needs no Docker:

```bash
PGT=~/.local/share/af-pgtest    # create it with initdb -U postgres --auth=trust
# ★ Start it on a unix socket, not TCP (-h '' closes TCP). On a shared development
#   host, a TCP port will collide sooner rather than later.
nohup "$PGT/dist/bin/postgres" -D "$PGT/data" -k "$PGT/sock" -h '' \
  -c shared_buffers=32MB -c fsync=off > "$PGT/pg.log" 2>&1 &
(cd control-plane && \
  AF_TEST_DATABASE_URL="postgres://postgres@/postgres?host=$PGT/sock&sslmode=disable" \
  go test -run 'TestPostgres|TestSchemaDialectParity' ./...)
"$PGT/dist/bin/pg_ctl" -D "$PGT/data" stop -m fast
```

- **CI**: `ci.yml` verifies fmt, vet, test and build for all three components on every
  push and PR. The end-to-end workflow is separate because building images is heavy.
  Upstream CLI breakage is a third system (below).
- **Console**:

```bash
npm --prefix console test
NODE_OPTIONS=--max-old-space-size=3072 npm --prefix console run build
```

A production build can exhaust the Node heap without that flag. Clean `gofmt`, clean
`go vet` and a clean `npm run build` are the bar for submitting
([CONTRIBUTING](../../CONTRIBUTING.md)).

> **Run `gofmt` before every commit — this is a hard gate.** `ci.yml` runs `gofmt -l .`
> per module and fails on a single unformatted file. `go build` / `go vet` / `go test`
> passing is **not** sufficient: an editor's auto-format missing a `_test.go` has
> actually slipped through. Confirm that `gofmt -l .` prints **nothing** in each module
> you touched.

### End to end: image smoke, fleet, UI, real API

Four layers. **L1** is the image smoke test (§10.3, seconds). **L2** is `e2e/` (a Go
module, standard library only). **L3** is `console-e2e/` (Playwright). **L4** uses real
credentials and is manual only.

- **L2** starts the CP headless, and through the public API alone starts a workspace,
  creates a **shell session**, types into it, reads the effect back through the
  filesystem API, and stops — against a real container. Being a shell session, it needs
  **no LLM credentials**.
- **L3** opens the Console in a real browser, opens a session, types into xterm, and
  observes the effect through the filesystem API — because xterm draws to a canvas and
  the characters cannot be read from the DOM.
- **L4** runs a headless agent turn inside a shell session to confirm the baked CLI can
  actually talk to its provider. **It consumes billing or subscription quota, so it is
  never on an automatic trigger.**

```bash
cd e2e && WS_IMAGE=agent-fleet/workspace:dev go test -v -tags e2e -timeout 15m
cd console-e2e && npm ci && npx playwright test
```

- Missing prerequisites cause a skip; CI escalates that to a failure.
- Safe to run on a development host with a live fleet: each layer uses a separate
  development user, ports are allocated dynamically, and teardown is built in. **One at
  a time** on a memory-constrained host.

### Detecting upstream CLI breakage

**Why end-to-end is not enough.** The e2e workflow passes no build args, so it always
verifies **the pinned version**. A workspace that opted into self-update installs
`@latest` at every start — **so the version CI looks at and the version the fleet runs
are different things**. On top of that, L4 runs headless, where no TUI and no footer is
drawn. Because of those two gaps, breakage in claude's state detection went **three
times** undetected by a green CI and was found by hand on the live fleet.

Two complementary systems close it — neither works alone:

| | version drift | contract tests |
|---|---|---|
| Watches | the version **number** (pin vs published latest) | the **behaviour**, against the real CLI |
| Answers | "is it time to look?" | "did it actually break?" |
| Cost | free | free to subscription quota, by tier |
| Frequency | daily | on relevant pushes, and weekly |
| Goes red | only if the check itself fails | when a contract breaks |

Drift is **the normal state** — some CLIs move every few days — so the drift workflow
does not go red. It keeps a single tracking issue up to date and closes it when the
drift clears. A second workflow compares the published versions daily and dispatches a
contract run **only for the CLIs whose version actually changed**, storing its state in
an append-only issue comment (repository variables cannot be written with the default
token). It records "tested" only on success, so a failure is retried the next day.

**One workflow file per agent.** Path filters and dispatch inputs are per workflow, so
putting them in one file means (1) unrelated changes trigger runs and (2) inputs get
mixed up — which really happened: two agents shared a single `live` input, and one
dispatch spent both quotas. Separate files make that coupling structurally impossible.
The cross-cutting exceptions are the daily drift watchers, and the MCP config contract,
which verifies one registry-side contract across several CLIs at once.

Shared setup lives in a composite action, parameterised by `pinned | latest | <version>`
so the same test can be aimed at "what we bake" or "what the fleet runs".

### Working in a public repository

This repository is public. **The secrets themselves are not in it** — they are stored
encrypted in the repository settings — and the following must stay true:

- **Fork PRs never get secrets.** The `pull_request` trigger does not pass them, and we
  rely on that. **`pull_request_target` and `workflow_run` are not used** — both are the
  classic hole of "give code written by a fork execution rights *with* secrets".
  No self-hosted runners either.
- **Jobs that use real credentials are `workflow_dispatch` only.** They never run on a
  PR, so a fork PR cannot go red for missing secrets.
- **Never interpolate `${{ github.event.* }}` into a `run:` block** — that is shell
  injection from a PR title.
- **Write secrets to a file; never to stdout.**
- **Treat artifacts and run logs as published.** Anyone can download them from a public
  repository. Secrets are masked on an exact match, but **values derived from them are
  not** — so, for example, the observed TUI frames of a signed-in session have the
  account name and email redacted before upload.
- **Declare `permissions:` in every workflow**, so least privilege survives a change of
  default.

**Credential leak detection.** In a public repository a leak is instantly public and
cannot be undone, so the scan covers **the entire history every time**, not the diff.
What matters:

- **Always pass `-m`.** Without it the scanner skips merge commits, and **anything
  introduced while resolving a conflict stays unscanned behind a green check** — this
  repository has 138 such merges, and the first scan really did have that hole.
- **Pin the scanner's version and checksum**; do not depend on a marketplace action.
- **Exclude false positives by the value, with a regular expression — never by path.**
  Excluding a path means a real secret in that file would go unnoticed.
- The first full-history audit found **zero real credentials**, cross-checked by
  expanding every reachable blob rather than walking the commit log.

## 10.5 Commits and branches

[CONTRIBUTING](../../CONTRIBUTING.md#commits--prs) is authoritative. The essentials:

- Small, focused commits. `<type>(<scope>): summary`, with **both the subject and the
  body in Japanese** for this repository's own history.
- An agent's commit carries a `Co-Authored-By:` naming **the model that did the work**,
  not the CLI.
- **Never commit secrets.** The env files and the allowlist are git-ignored; check the
  diff before committing.
- **Keep the core deployment-independent**: no Docker or compose assumptions inside the
  CP's core — put them behind a port ([09 §9.2](09-deploy.md)).
- A migration must be forward compatible (applied automatically at start, no
  downgrade), and the commit must say so.
- Record how you verified it — tests, and for a behaviour change, what you actually
  observed.

## 10.6 Documentation

What to update when you change what is the table in [README](README.md). The norms
every shelf follows are [CONVENTIONS](../CONVENTIONS.md).
