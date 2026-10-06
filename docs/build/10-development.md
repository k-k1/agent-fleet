---
audience: "someone building this repository for the first time"
source_of_truth: "the code and the CI definitions"
updated: "2026-10"
---

# 10. Development — building, reflecting a change, testing, conventions

English | [日本語](10-development.ja.md)

## 10.1 Repository layout (responsibilities only)

| Directory | Responsibility |
|---|---|
| `console/` | The browser SPA (React + Vite + zustand). The CP serves its build output, `console/dist`, as static files |
| `control-plane/` | The Control Plane (Go, its own module). Migrations are embedded and applied at start |
| `workspace/` | The workspace image (Dockerfile, entrypoint, the opencode plugin, the workspace notes) plus `workspace/agent/`, the agent as a separate Go module |
| `deploy/` | The deployment layer (`local` / `compose` / `aws` / `native`) and the release tooling (`release/`). The runbooks are the READMEs there ([09](09-deploy.md)) |
| `e2e/` | Fleet end-to-end tests (a separate Go module, standard library only) — the CP against real containers (§10.4) |
| `console-e2e/` | Console UI end-to-end tests (Playwright) — browser through CP to a real container (§10.4) |
| `guide/` · `docs/` | The user guide that ships inside every container, and the developer documentation. The norms for both are [CONVENTIONS](../CONVENTIONS.md) |
| `scripts/` | Repository checks: `docs-check.py` (links, front matter, the `guide/ref` tables), `vet-build-tags.sh` and `model-id-lint/` (model ids outside the fallback registry) |

The file-level map is [90-code-map](90-code-map.md).

## 10.2 What to do to see a change

**The key fact: a new image only takes effect on Stop → Start.** `docker run` does
nothing to a container that is already running; on the docker runtime, Start removes
the container and runs it again, so the swap is guaranteed. The home directory
(logins, connections, repositories) is a bind mount there and is unaffected by an image
update. When the image a Start would use differs from the one a workspace is running,
the Console shows a **Restart needed** badge (`control-plane/workspace_stale.go`).

| What you changed | What it takes |
|---|---|
| Console (`console/src`) | `npm --prefix console run build` (or `run dev`, which is `vite build --watch`) → **reload the browser**. The CP reads `console/dist` from disk, and its caching headers ([05 §5.4](05-api.md#54-cross-cutting-rules)) let a reload pick up the new build, so the CP does not need restarting |
| The CP's Go | rebuild and restart the CP (`restart-cp.sh`). No image rebuild |
| The agent's Go, or anything in the image | rebuild the image (`run-dev.sh`) → each user does **Stop → Start** from the Console. The CP never force-swaps a running workspace. Under `native` there is no image: `run-dev.sh native` rebuilds the agent binary instead |
| The pinned version of an agent CLI, `rtk`, `gh` or Go | follow the runbook in §10.2.1 |
| Anything the entrypoint applies (seeded config, timezone…) | Stop → Start only; no rebuild |
| The shared JVM (docker runtime) | delete the shared directory and re-provision it (`deploy/local/provision-jvm.sh`, which skips a directory that already holds JDKs) |

### 10.2.1 Bumping a pinned tool — the standard procedure

Follow this when raising the version of `rtk`, `gh` or Go, or an agent CLI by hand. For
the agent CLIs the usual route is the bump PR that §10.2.2 opens automatically; steps 1–3
below are what it does for you, and steps 4 onward still apply to it. Every version
is a build argument in `workspace/Dockerfile`, because an unpinned `npm install -g`
hits the Docker layer cache and **a rebuild does not actually raise the version**. How
the pins reach a workspace — baked into the image, or boot-installed from the version
manifest `versions.json` on the default lean image — is
[04 §4.9](04-agent.md#49-the-workspace-image-and-its-entrypoint).

1. **Check what latest is.** `deploy/local/cli-drift-check.sh [cli]` prints the pin and
   the published latest for every agent CLI and `rtk`, reading the same release sources
   as CI. For `gh`, its releases page. For Go, keep step with the `go` directive in
   `workspace/agent/go.mod` — if you are not raising that, leave it.
2. **Bump the build argument** in `workspace/Dockerfile`. The CLIs that ship as
   binaries (agy, cursor, kiro, muse) also pin a sha256 per architecture; the comment
   above each argument says how to get them. Changing an argument reliably breaks the
   cache, so `--no-cache` is unnecessary.
3. **Commit and push** — a small diff, with a message that follows
   [CONTRIBUTING](../../CONTRIBUTING.md#commits--prs).
4. **Wait for the end-to-end workflow to go green.** `e2e.yml` does **not** run on a
   push (only on PRs to main, a nightly cron and manual dispatch), so start it on your
   branch yourself: `gh workflow run e2e.yml --ref <branch>`. It builds the image with
   the CLIs baked in (`BAKE_AGENT_CLIS=1`) and verifies L1 (**the installed versions
   equal the pins**, for the tools §10.3 names), L2 (fleet connectivity) and L3 (Console UI). **Do not proceed
   while it is red.**
5. **(Larger bumps) run that CLI's contract** (below). Each CLI has its own workflow
   and inputs, so run only the one you need. The ones that take real turns spend that
   CLI's subscription quota. Both `live` inputs default to false. On `e2e.yml`, `live`
   is L4, a headless claude turn. On `codex-contract.yml`, it is Tier 2 (the workflow
   records roughly 45k tokens per run, measured). Neither draws the TUI, so state
   detection is the contract workflows' job, not L4's.
6. **Reflect it on the host** with `run-dev.sh`. The image smoke test (L1) runs right
   after the build. On the default lean image it checks that `versions.json` carries
   the new pins and that no CLI is baked in; with `BAKE_AGENT_CLIS=1` it also checks
   the installed versions.
7. **Reflect it in each workspace**: every user does **Stop → Start** from the Console.
   On a lean image, while self-update is off, the entrypoint moves the boot-installed
   CLIs to the new pin in `~/.local` at a start the member makes (it needs the network;
   a failure is retried at the next start). With self-update on they stay on latest
   (see the note below). kiro and muse are installed on demand instead, and follow the pin through
   their own installers ([04 §4.9](04-agent.md#49-the-workspace-image-and-its-entrypoint)).
   Home and repositories survive.
8. **(Optional) confirm** from **Settings → Toolchains → Tool versions**, which shows
   the effective, image and pinned versions side by side.

Two notes:

- **A nightly scheduled run** of `e2e.yml` (04:00 JST, against develop) catches
  upstream CLI and base-image breakage even when nothing in this repository changed. If
  it goes red, suspect upstream and use steps 4–5 to isolate.
- To move a single member forward without rebuilding, there is an opt-in self-update: a
  tenant setting allows it, and the member turns it on per workspace under Settings →
  Toolchains. A start the member makes then installs latest into `~/.local`; an
  unattended start (a scheduled run's wake) skips the update and keeps what is
  installed. Turning it off and doing Stop → Start returns the workspace to the pin.

**Is the thing that tells you to bump still running?** `cli-release-watch.yml` is what
notices a public version moved, and the version its contract last passed is the `tested`
state in the tracking issue. A `tested` that stops moving reads identically whether
upstream went quiet or the job fell over — on 2026-09-09 it was the latter, and it took
until the next morning to notice. So **Settings → Toolchains** carries one line under
the tool-version table: `Upstream release watch: last clean run <relative time>`. It
warns only when the watcher named a source it could not read, or has had no clean run
for 48 hours. The CP reads the issue anonymously once an hour and caches it
(`control-plane/cli_release_watch.go`); where it cannot reach GitHub the line is absent
rather than either reassuring or alarming. **When the line warns, step 1 above is not
enough** — the drift check says what latest is, but the watcher is what would have
dispatched the contract, and nothing is being tested while it is down.

### 10.2.2 Automated agent-CLI bumps (`cli-pin-bump.yml`)

`cli-pin-bump.yml` keeps **one** pull request against `develop`, on the fixed branch
`automation/cli-pin-bump`, that raises every agent-CLI pin whose new version has already
passed its contract. It runs when a contract workflow completes, every 2 hours (odd hours) as a
backstop, and on dispatch. The decision and the edit are `deploy/local/cli-pin-bump.sh`;
`deploy/local/cli-pin-bump-stub-test.sh` pins them.

- **Which kinds move.** A kind is bumped only when its pin differs from the public latest
  **and** the release-state issue records `tested` for exactly that latest. Every
  contract records `tested` only after a passing `latest` run, never for a pinned-version
  run. For claude that contract is `claude-tui-contract.yml`, which drives the real TUI
  against the footer and spinner detection that
  `workspace/agent/internal/tmuxx/testdata/footers/SOURCE.txt` documents. Every drifting
  kind that is left out is listed in the PR body with the reason. The gate is equality,
  not a version comparison, so a kind that publishes again before its bump is merged
  drops out of the PR until its contract passes on the new latest (the watcher dispatches
  it the same day). When nothing is left to bump, an open bump PR is closed with the
  reason. The state issue is public, so only markers written by a workflow
  (`github-actions`) or by an owner, member or collaborator count, and only in the issue
  the workflows opened; anyone else's `tested` comment is ignored.
- **Checksums** come from the sources the Dockerfile comments name: agy's per-arch
  manifests (both archives are downloaded, checked against the manifest's sha512 and
  hashed to sha256; the release build id comes from the manifest URL); kiro's stable
  manifest and muse's versioned release manifest, where the x86_64 download must hash to
  the published value; cursor publishes none, so both tarballs are hashed as downloaded.
  A mismatch, a value that is not a sha256, or a manifest that has moved on to another
  version leaves that kind at its pin. A source or download that could not be read is
  different: the run then leaves the branch and the PR as they are until a complete
  run, so a network blip never drops a kind or closes the PR. The npm kinds have no
  checksum.
- **The edit** touches only the bumped kinds' `ARG` lines, is checked line by line
  before it replaces the file, and a second run changes nothing.
- **The PR** carries the evidence table — each kind's `pin → latest`, a link to the
  contract run that recorded `tested` (`cli-release-state.sh` writes the run URL into the
  marker comment), and where the checksums came from. The branch is force-updated only
  when the Dockerfile edit changes. Nothing merges it: review it, wait for CI and follow
  §10.2.1 from step 4. Once it is merged, `cli-drift.yml` closes the drift issue on its
  next run if no other pin is behind. Closing it without merging declines that exact edit
  (recognised by an id in the body, even after the branch is deleted) until a version
  changes.

**What stays manual:**

- **cursor and kiro** credentials rotate, so the watcher never dispatches their
  contracts. Refresh the secret, then `gh workflow run cursor-contract.yml -f
  cli_version=latest` (or `kiro-contract.yml`); the bump follows when it passes.
- **claude, copilot and agy** are dispatched only while their contract secrets are set;
  without one, run the fleet probe from the companion issue and dispatch the contract
  once the secret is back.
- **rtk** has a release source but no contract, so there is no `tested` to gate on. It
  is listed as drifting and bumped by hand (§10.2.1).

**The token (one-time setup, by a repository admin).** A push or PR made with
`GITHUB_TOKEN` starts no other workflow, so the bump PR's CI would never run. The job
therefore pushes and opens the PR with the Actions secret **`CLI_PIN_BUMP_TOKEN`**, and
fails with an error naming it when it has something to push and the secret is missing.
Create a **fine-grained personal access token** with *Repository access* limited to this
repository and the permissions **Contents: Read and write** and **Pull requests: Read and
write** (nothing else). A GitHub App with the same two permissions, installed on this
repository only, works too, but its tokens last an hour, so it needs a token-minting step
(`actions/create-github-app-token`) in front of the push instead of a stored secret.

Save it under **Settings → Secrets and variables → Actions → New repository secret** as
`CLI_PIN_BUMP_TOKEN`. A PAT expires; when it does, the job goes red with the same
message, and the drift issue keeps reporting the pins as behind.

## 10.3 What the start scripts do (`deploy/local/`)

- **`run-dev.sh`** — the **single entry point**, with subcommands. It prepares the
  workspace runtime (the shared JDKs, the workspace image and its smoke test), builds
  the Console, builds the CP, and starts the CP as a host process. It sources the
  git-ignored `deploy/local/oauth.env` when present (template:
  [`oauth.env.example`](../../deploy/local/oauth.env.example)), so authentication and
  crypto settings reach the CP; without it you get a plain dev start. The image is
  lean unless you set `BAKE_AGENT_CLIS=1`; `WS_SMOKE=0` skips the smoke test.

  | Subcommand | What it does |
  |---|---|
  | (none) / `local` | the development default, Docker runtime |
  | `wsl` | a WSL preset (Docker and cgroup preflight, `AUTH=dev` fixed) |
  | `native` | containerless, no Docker (single user — [ref/deploy-targets](../../guide/ref/deploy-targets.md)). Builds the agent on the host and hands it over |
  | `reset [--all] [--yes]` | wipe local data: by default the dev user's workspace only (the DB and shared JDKs are kept); `--all` wipes the whole data directory. Refuses while the CP is running, and cleans up the leftovers of both runtimes before deleting |

  ⚠️ With no subcommand, the env value `AF_RUNTIME` decides, and `AF_RUNTIME=wsl` is
  an alias for *containerless* — a different thing from the `wsl` subcommand (a
  Docker preset). Prefer the subcommand; the two are easy to confuse.

- **`restart-cp.sh`** — the light path: rebuild only the Console and the CP, swap the
  running CP process in place, and wait for `/healthz`. **It does not rebuild the
  workspace image.** `SKIP_CONSOLE=1` limits it to the Go side. It reproduces
  `run-dev.sh`'s environment, and requires `oauth.env` to exist.
- **`e2e-smoke.sh`** — the image smoke test (L1), run with `docker run` against a
  built image. On a baked image it checks that the installed claude, opencode, codex,
  copilot, cursor, kiro, muse, agy and rtk match the Dockerfile's pins (that is, the
  cache is not stale), as do Go, `gh` and Chromium on every image. agy is asked through
  the same `OPENSSL_ia32cap` mask the Agent uses when the host has no RDRAND, and rtk
  may instead be absent with an `rtk-unavailable` marker (arm64). On a lean
  image it checks that no CLI is baked in. Either way it checks `versions.json` against
  the pins and that the image's own files (the agent, the entrypoint, the policy
  `CLAUDE.md` and the like) are present. `run-dev.sh` runs it after
  every build; `deploy/local/e2e-smoke.sh [image]` runs it alone.

Host-specific practice (PATH, docker group membership and so on) is each host's own
business and is not recorded here.

## 10.4 Testing

The product is **two Go modules**, run separately (`e2e/` and the release scanner in
`deploy/release/scan/` are modules of their own):

```bash
(cd control-plane   && go test ./...)
(cd workspace/agent && go test ./...)
```

- The CP side carries many `httptest`-based smoke tests (audit, egress, the internal
  git smart-HTTP and LFS, among others). The Postgres tests skip themselves unless
  `AF_TEST_DATABASE_URL` is set, and CI does not set it.
- ⚠️ **When you add a migration, run it once against a real Postgres** — why is
  [06 §6.5](06-data.md#65-migration-practice). The `-run` pattern below matches four
  tests (`TestPostgresStore`, `TestPostgresPasswordRotation`,
  `TestPostgresDeleteCascade`, `TestSchemaDialectParity`). In a Workspace, `af-db`
  handles install, init and start, and the completion criterion is 4 PASS, 0 SKIP:

```bash
# In a Workspace (af-db available):
(cd control-plane && \
  AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 \
  -run 'TestPostgres|TestSchemaDialectParity' ./...)
af-db down    # stop it before the next heavy build
```

  `af-db` uses scram-sha-256 authentication, so `TestPostgresPasswordRotation` runs
  (it skips on a server that uses trust auth). `-count=1` defeats the test cache — a
  cached `ok` proves nothing. Outside a Workspace, point `AF_TEST_DATABASE_URL` at any
  disposable Postgres you run yourself. On a shared host, a unix socket avoids port
  collisions. With trust auth, expect 3 PASS and 1 SKIP.

  The tests never touch `public`: every test that migrates or writes rows takes a fresh,
  uniquely named schema from `pgtest.Schema` (`control-plane/internal/pgtest`), whose
  connections have `search_path` set to it alone, and drops it when the test ends. So
  overlapping runs — two sessions on one database, or `go test ./...` running packages in
  parallel — cannot drop each other's tables, and a schema left behind fails the test. A new
  Postgres test goes through that helper rather than reading `AF_TEST_DATABASE_URL` itself,
  and the URL must not set `search_path`.

- **Console**, from the repository root:

```bash
npm --prefix console test
npm --prefix console run build
```

  The `build` script raises the Node heap itself. The split into a `node` and a `dom`
  test project, and why the tests must run with `console/` as the working directory,
  are in [AGENTS.md](../../AGENTS.md#running-the-console-tests).

- **The bar for submitting** — clean `gofmt`, clean `go vet` and a clean
  `npm run build` — and the pre-commit hook that runs the forbidden-token scan on what
  you stage are in [CONTRIBUTING](../../CONTRIBUTING.md#ground-rules). **`gofmt` is a
  hard gate**: `ci.yml` fails on a single unformatted file, even when `go build`,
  `go vet` and `go test` pass.

- **CI** — `ci.yml` runs on every push to and PR against `main` and `develop`, in
  independent jobs:

  | Job | What it checks |
  |---|---|
  | `control-plane`, `workspace-agent` | `gofmt -l`, `go vet`, `go build` (also cross-compiled for arm64), `go test`, and `go vet` over the build-tagged files. The agent job also syntax-checks `entrypoint.sh` |
  | `console` | typecheck, lint, the i18n lint, the model-id lint, vitest, two real-browser checks (`pdf:check`, `doc:check`) and the production build |
  | `deploy-scripts` | the deployment scripts and the release watcher's decisions against stubbed `aws` / `npm` / `curl` / `gh`, and that the CloudFormation templates are ASCII-only |
  | `secret-scan` | credential leaks over the whole history (below) |
  | `release-scan` | the forbidden-token gate over the tracked tree — the same scanner the pre-commit hook runs over staged content |
  | `model-id-lint` | no model-id-shaped string literal in either Go module outside `workspace/agent/internal/modelfallback`; `// model-id-lint:allow <reason>` marks one that chooses no model |

  On a pull request, the `changes` job skips `control-plane`, `workspace-agent`,
  `deploy-scripts` and `console` when every changed path is inert — `docs/` and `guide/`
  (except the files a test reads: `guide/ref/agents{,.ja}.md` and
  `docs/decisions/0029-usage-accounting{,.ja}.md`), the top-level `*.md`
  files and the docs checker. The list and the reason are in `scripts/ci-changes.sh`.
  Pushes to `main` / `develop` always run every job.

  `docs.yml` runs `scripts/docs-check.py` on the same triggers. The end-to-end
  workflow is separate because building images is heavy. Upstream CLI breakage is a
  third system (below). The workflows that bake images (for example the workspace
  image for a development deployment, and the engine images) and `publish-dist.yml`
  run on dispatch only; `release-gate.yml` runs on dispatch, and on a push that changes
  it on the packaging branch (see
  [deploy/release/notes](../../deploy/release/notes/README.md)).

### End to end: image smoke, fleet, UI, real API

Four layers. **L1** is the image smoke test (§10.3, seconds). **L2** is `e2e/` (a Go
module, standard library only). **L3** is `console-e2e/` (Playwright). **L4**
(`e2e/live_test.go`) uses real credentials and is manual only.

- **L2** starts the CP headless, and through the public API alone starts a workspace,
  creates a **shell session**, types into it, reads the effect back through the
  filesystem API, and stops — against a real container. Being a shell session, it needs
  **no LLM credentials**.
- **L3** opens the Console in a real browser, opens a session, types into xterm, and
  observes the effect through the filesystem API — because xterm draws to a canvas and
  the characters cannot be read from the DOM. It needs a built `console/dist`, and uses
  `/usr/bin/chromium` unless `E2E_CHROMIUM_PATH` names another.
- **L4** runs `claude -p` inside a shell session to confirm the claude CLI can actually
  talk to Anthropic. It runs only when `E2E_ANTHROPIC_API_KEY` (metered) or
  `E2E_CLAUDE_OAUTH_TOKEN` (a `claude setup-token` token, subscription quota) is set.
  **It consumes billing or subscription quota, so it is never on an automatic
  trigger.**

```bash
cd e2e && WS_IMAGE=agent-fleet/workspace:dev go test -v -tags e2e -timeout 15m ./...
cd console-e2e && npm ci && npx playwright test
```

- `e2e.yml` runs on PRs to main (relevant paths), a nightly cron against develop, and
  manual dispatch — **never on a push**. Its jobs are `e2e` (L1 → L2) and `ui-e2e`
  (L3, which uploads traces and the CP log on failure), in parallel, and `live-smoke`
  (L4), only on dispatch with `live`. Its image build passes no platform argument, so
  it covers **amd64 only**: the arm64 assets (the per-arch sha256 pins for agy, cursor,
  kiro and muse) are not build-verified there.
- Missing prerequisites (docker, a built image, and for L3 `console/dist`) cause a
  skip; CI sets `E2E_REQUIRE=1`, which turns that into a failure.
- Safe to run on a development host with a live fleet: each layer uses a separate
  development user (`e2e`, `e2e-ui`, `e2e-live`), ports are allocated dynamically, and
  teardown is built in. **One at a time** on a memory-constrained host.

### Detecting upstream CLI breakage

**Why end-to-end is not enough.** `e2e.yml` passes no version build arguments, so it
always verifies **the pinned version**. A workspace that opted into self-update installs
latest at start — **so the version CI looks at and the version the fleet runs are
different things**. On top of that, L4 runs headless, where no TUI and no footer is
drawn. Because of those two gaps, breakage in claude's state detection
(`workspace/agent/internal/tmuxx`) went **three times** (as of 2026-07-17) undetected by
a green CI and was found by hand on the live fleet.

Two complementary systems close it — neither works alone:

| | version drift (`cli-drift.yml`) | contract tests (one workflow per CLI) |
|---|---|---|
| Watches | the version **number** (pin vs published latest) | the **behaviour**, against the real CLI |
| Answers | "is it time to look?" | "did it actually break?" |
| Cost | free | free to subscription quota, by tier |
| Frequency | every 2 hours | PRs to main (relevant paths), a weekly cron, and dispatch (claude, copilot, agy, cursor and kiro are dispatch-only) |
| Goes red | only if the check itself fails | when a contract breaks (a step that depends on an outside service can be report-only, e.g. opencode's live Tier B turn) |

Drift is **the normal state** — some CLIs move every few days — so the drift workflow
does not go red. It keeps a single tracking issue up to date and closes it when the
drift clears. The rows it checks are `TARGETS` in `deploy/local/cli-drift-check.sh`:
every agent CLI whose version is pinned, plus `rtk`. lcpp is not among them: it runs
in-process against a self-hosted engine and has no upstream CLI.

The same workflow's `apt-pins` job checks the Debian package pins with
`deploy/local/apt-pin-check.sh`. It reads them out of every `apt-get install` in
`workspace/Dockerfile` (today only `ARG CHROMIUM_VERSION`): every package of a pin must still be listed for
amd64 **and** arm64 in trixie, trixie-updates or trixie-security. trixie-security keeps
only the current build, so a pin disappears as soon as Debian ships the next update — for
one architecture first, sometimes — and the next uncached image build fails. That is a
broken build, not steady-state drift, so this job goes red; its output names the newest
version served on both architectures, which is what the ARG is bumped to by hand.
`deploy/local/apt-pin-check-test.sh` pins its verdicts.

A second workflow, `cli-release-watch.yml`, compares the published versions every 2 hours (drift at :00, the watcher at :30, the
pin-bump backstop at the next odd hour) and
dispatches a contract **only for the CLIs whose version actually changed** (the kinds
are `KINDS` in `deploy/local/cli-release-edges.sh`). Its state lives in one issue:
`tested` and `seen` markers are appended as comments, because repository variables
cannot be written with the default token (`deploy/local/cli-release-state.sh`). It
records `tested` only when the contract succeeds. A contract that finishes red for
`latest` writes a `red` marker for that version (and a row in the `Red contracts` section
of the drift tracking issue, which also flags a contract workflow that failed its last 2
runs on `develop`; `deploy/local/cli-contract-report.sh`), and the watcher does not
dispatch that version again, so a failing release does not spend the contract's quota
12 times a day. A new release, a passing run (`tested == latest` wins) or a manual dispatch
releases it; a cancelled or timed-out run leaves no marker and is retried.
A CLI whose credential cannot be supplied unattended is not dispatched. This covers a
missing secret, and cursor and kiro, whose credentials rotate. For those it records
`seen`, and the contract is dispatched by hand once the secret is refreshed — "detected"
is never recorded as "tested". muse needs no credential for its contract, so it is
dispatched unattended.

**One unreadable release source does not stop the watcher.** Each row is fetched
independently; a row that cannot be read is reported as unknown and only that row's
dispatch and state update are skipped, while the rest of the run proceeds. The watcher
goes red only when not one source answered. The rows that could not be read are named
in the job summary, and every run writes its own liveness — last clean fetch, last
failing rows — into the `watcher` block of the state issue's body, so a `tested` version
that has stopped moving can be told apart from an upstream that has stopped releasing.
`deploy/local/cli-drift-stub-test.sh` pins both decisions. The last step, the bump PR
itself, is `cli-pin-bump.yml` (§10.2.2).

**One workflow file per CLI** (`claude-tui-contract.yml`, and `<kind>-contract.yml` for
the others). Path filters and dispatch inputs are per workflow, so putting them in one
file means (1) unrelated changes trigger runs and (2) inputs get mixed up — which really
happened: codex's Tier 2 and claude's L4 shared a single `live` input, and one dispatch
spent both quotas. Separate files make that coupling structurally impossible. The
cross-cutting exceptions are the two scheduled watchers, and `mcp-config-contract.yml`
(no credentials), which verifies one registry-side contract across several CLIs at
once: the shape of the global MCP config file af writes for each CLI. CI covers
claude, codex, opencode, copilot and cursor; kiro (needs a login) and agy (will not
start on the runner) are not covered there. muse and lcpp have no such file — they get
their servers on the wire and in process (`ServedKinds` in
`workspace/agent/internal/mcpreg/materialize.go`).

Shared setup (Go, Node, tmux and the real CLI) lives in the composite action
`.github/actions/setup-agent-cli`, parameterised by `pinned | latest | <version>` (an
explicit version for the npm CLIs only), so the same test can be aimed at "what we
bake" or "what the fleet runs". muse's contract installs the release artifact itself,
because verifying the manifest's checksum is part of what it tests.

**Build tags.** The Go tests that need something a plain `go test ./...` cannot assume sit
behind one of three build tags in `workspace/agent`. The tag says what running the test
costs; which CLI it exercises is in the test name (`TestDriftCodex…`, `TestContract…`,
`Test<Kind>TUIMirrorContract`), and every workflow selects its tests with `-run`.

| Tag | Needs | Without it |
|---|---|---|
| `contract` | the real CLI on `PATH` (and tmux for the pane tests). Some tests that spend a turn also wait for their own opt-in (`CLAUDE_CONTRACT_LIVE`, `COPILOT_CONTRACT_LIVE`, `OPENCODE_CONTRACT_LIVE`, `AF_IMAGEGEN_LIVE`); the TUI probes (`TestClaudeTUIContractLive`, `TestClaudePlanApprovalContractLive`, `Test<Kind>TUIMirrorContract`) do not, and run a real turn wherever the CLI is signed in | skips, or fails under `E2E_REQUIRE=1` |
| `contract_live` | real codex credentials; every test spends real turns (`codex-contract.yml`'s `live-drift`, dispatch only) | fails |
| `contract_manual` | a person: an engine endpoint they provide (`AF_LCPP_LIVE_*`) or an interactive sign-in (`AF_AGY_LOGIN`); no workflow runs it | skips |

On a machine where the CLIs are signed in, a bare `go test -tags contract ./...` therefore
spends real turns on several vendors at once: narrow it with `-run` to one CLI, as the
workflows do.

`ci.yml` vets all three through `scripts/vet-build-tags.sh`, which also fails on any tag it
does not know, so a new tag is added there or it goes red. The `e2e` module has its own
`e2e` tag (§10.4, end to end).

### Working in a public repository

This repository is public. **The secrets themselves are not in it** — they are stored
encrypted in the repository settings — and the following must stay true:

- **Fork PRs never get secrets.** The `pull_request` trigger does not pass them, and we
  rely on that. **`pull_request_target` and `workflow_run` are not used** — both are the
  classic hole of "give code written by a fork execution rights *with* secrets".
  No self-hosted runners either.
- **Jobs that authenticate with real credentials run only on `workflow_dispatch`** (for
  example `e2e`'s `live-smoke`, `codex-contract`'s `live-drift`, and the dispatch-only
  contracts). No job a pull request triggers reads a credential secret, so a fork PR
  cannot go red for missing secrets. The scheduled release watcher only checks whether
  those secrets exist before it dispatches.
- **Never interpolate `${{ github.event.* }}` into a `run:` block** — that is shell
  injection from a PR title.
- **Write secrets to a file; never to stdout.** For example, kiro's auth database is
  stored as eight base64 parts and decoded straight into a file.
- **Treat artifacts and run logs as published.** Anyone can download them from a public
  repository. Secrets are masked on an exact match, but **values derived from them are
  not** — so, for example, the observed TUI frames of a signed-in session have the
  account name and email redacted before upload.
- **Declare `permissions:` in every workflow**, so least privilege survives a change of
  default.

**Credential leak detection** (`ci.yml`'s `secret-scan`, gitleaks). In a public
repository a leak is instantly public and cannot be undone, so the scan covers **the
entire history every time**, not the diff. What matters:

- **Always pass `-m`.** Without it the scanner skips merge commits, and **anything
  introduced while resolving a conflict stays unscanned behind a green check** — when
  the scan was introduced this repository had 138 such merges, and the first scan
  really did have that hole.
- **Pin the scanner's version and checksum**; do not depend on a marketplace action.
- **Exclude false positives by the value, with a regular expression — never by a path
  alone** (`.gitleaks.toml`; a rule may narrow a value to one file with
  `condition = "AND"`). Excluding a path means a real secret in that file would go
  unnoticed.
- The first full-history audit (2026-08-01) found **zero real credentials**,
  cross-checked by expanding every reachable blob rather than walking the commit log.

## 10.5 Commits and branches

[CONTRIBUTING](../../CONTRIBUTING.md) owns these rules. Its
[Ground rules](../../CONTRIBUTING.md#ground-rules) cover secrets, the pre-commit hook,
keeping the core deployment-independent and `gofmt`. Its
[Commits & PRs](../../CONTRIBUTING.md#commits--prs) section covers the trunk and
release branches, the message format and language, the forward-compatibility note a
migration needs, and the `Co-Authored-By` attribution. [AGENTS.md](../../AGENTS.md)
repeats what an agent needs at commit time and points back there.

## 10.6 Documentation

What to update when you change what is the
[update-trigger table](README.md#update-trigger). The norms every shelf follows are
[CONVENTIONS](../CONVENTIONS.md).

### Rewriting the Japanese Console catalogue (`catalog-diff-check.py`)

A wording pass over `console/src/lib/i18n/locales/ja/<domain>.ts` may change prose and
nothing else. `scripts/catalog-diff-check.py <ref>` compares the files at `<ref>` with the
working tree and fails on a change outside string-literal contents (keys, order, comments,
`+` structure), on a changed key set, on any edit under `locales/` outside `ja/`, and, per
changed value, on any difference in the multisets of `{placeholders}`, Trans slots,
digits, ASCII words, 「…」 contents, `code` spans, line breaks, edge whitespace and the
terms of `guide/ref/glossary.ja.md`. Reword a UI label (at most 15 characters, no 「。」)
only on purpose: it fails without `--allow-labels`, and with it every `old -> new` pair is
printed for review. A changed value whose old text is still quoted in `guide/**/*.ja.md`,
console tests, Go sources or `workspace/agent/knowledge/af-usage.md` is reported as PINNED
with `file:line` and fails; update that citation in the same PR (`--list-pinned` prints
just those locations). A short common-word label can also be quoted for another purpose;
after reading the hit, accept exactly that one with `--exempt-pin KEY@PATH[:LINE]` (printed
as EXEMPT and counted, other hits still fail). A guide quote that reproduces only the start of a sentence, or any
quote that does not contain a whole old clause, is not searched; after a rewrite, search
the guide by hand for the opening words of each changed value. It does not judge meaning, and, like
`scripts/guide-diff-check.py`, it is for local use and is not part of CI. Its tests are
`python3 -m unittest discover -s scripts -p test_catalog_diff_check.py`.

#### `--lang en`: the English catalogue

`--lang en` points the same script at `locales/en/<domain>.ts` to guard a wording pass over
the English strings. ja is the canonical source and en is derived from it, so a rewrite must
keep meaning parity with ja; the script holds the structure and the facts, a reviewer judges
the meaning. The default (no flag) is the ja mode above, unchanged. In en mode any change
outside `en/` (`ja/` included) fails, and the key, order and file rules are the same. Per
changed value, instead of the ASCII-word rule it compares the multisets of `{placeholders}`,
Trans slots, digits, `code` spans, line breaks and edge whitespace, plus ALL_CAPS words,
identifiers (paths, env vars such as `AF_MASTER_KEY`, snake_case and dotted names, `--flags`,
CLI and product names such as `codex`, case included), the contents of `"…"` quotes and the
→ and ⚠ marks. The glossary is the Screen column of `guide/ref/glossary.md`, matched
case-insensitively on word boundaries (a plural `s` is the same word). A label is at most 30
characters with no sentence-ending punctuation (`--allow-labels` as before). PINNED splits the old
text at sentence ends, `{x}` slots and line breaks (clauses of 20+ characters) and searches
`guide/**/*.md` except `*.ja.md` and `README*.md`, console tests and Go sources (not `workspace/agent/knowledge/af-usage.md`, a Japanese
document); a label is also looked for as `**label**`, `"label"`, `'label'` and `` `label` ``;
`--list-pinned` and `--exempt-pin` work as in ja mode. A change in the count of a restriction
word (only, never, must, not, cannot, default, required, unless, except; `can't` and `n't`
count as their long forms) in a changed value prints `WARN restriction` and is totalled on a
`warnings:` line; it never fails the run, but the rewrite deserves a human look.
`--triples` prints `key`, the current ja value, the old en and the new en for every changed
value as TSV (tabs and line breaks escaped) and exits 0, for the reviewer comparing meaning
against ja. Not part of CI.

**Approving an intended term change** (`--allow-term`, `--allow-terms-file`). A rewrite that
applies a terminology decision changes the glossary counts (and, in ja, the Latin-word counts:
`OFF` → `オフ`), which the checks above report. Approve exactly that change, per key:

```
--allow-term KEY:OLD>NEW[*N]              repeatable; N defaults to 1
--allow-terms-file PATH                   lines KEY<TAB>OLD<TAB>NEW[<TAB>N]; blank lines and # comments skipped
```

The allowance holds only if, in that key's changed value, the count of OLD fell by exactly N
and the count of NEW rose by exactly N, in every category that counts the term (the glossary,
and the Latin-word rule in ja or the ALL_CAPS rule in en; a term no category counts is
counted directly). Everything else in the value is still held to the unchanged checks, the
same term changing in another key still fails, and `--allow-labels`, PINNED and the other
categories are not relaxed. Each applied allowance is printed (`ALLOWED term: … OLD -> NEW xN
(OLD a -> b, NEW c -> d)`). An allowance that did not apply (wrong direction, wrong count, the
key unchanged or absent) is `FAIL allow` with the counts it saw, and the drift it was meant
to explain fails too. The `failures:` line gets an `allow=` entry only when an allowance is
given, so a run without the flags prints exactly what it printed before. Counting rules are
the existing ones (substring in ja, so `既定` inside `既定値` counts; word boundary in en).

A term that two categories count (an en ALL_CAPS glossary term such as `DEFAULT`: glossary,
case-insensitive, and caps, exact) is summed per (category, unit), so `default DEFAULT` →
`standard standard` needs both `Default>standard` and `DEFAULT>standard`. Messages name the
category (`[glossary]`, `[caps]`, `[latin]`, `[direct]` for a term no category counts).

Allowances of one key that repeat the same OLD and NEW add up (two `OFF>オフ` equal `*2`). A
term that is OLD in one allowance and NEW in another of the same key (reversed or chained) is
refused with exit 2, because the two would cancel; state the net change as one allowance. In
the TSV file the fields are taken as they are, so terms may contain `>`, `*` or `:`; on the
command line `KEY` ends at the first `:`, `OLD` at the first `>`, and a trailing `*digits`
on NEW is the count (use the file for a term that ends that way).

Worked example (ja; `既定 OFF。` → `既定ではオフです。`, `デフォルト` → `既定`):

```
python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
  --allow-term 'agents.note_x:OFF>オフ' --allow-term 'surface_color.default:デフォルト>既定'
```

Without the flags this reports `FAIL latin` (`OFF` removed), `FAIL glossary` (`オフ` 0 → 1)
and `FAIL glossary` (`既定` 0 → 1); with them, two `ALLOWED term:` lines and exit 0. Applying
the same decision to a second key needs a second allowance. en works the same way
(`--lang en --allow-term KEY:OFF>off`; glossary terms are matched case-insensitively).

**Label sync** (`--list-citations`, `--rewrite-guide`, `--allow-split`). A label
rewrite must keep every citation in sync in the same PR. The always-on SPLIT check
looks across **all domains of the selected language**, even when explicit file
arguments restrict the review: every key with the same old label must change to the
same new text. Otherwise `FAIL split` lists the divergent keys and exits 1.
`--allow-split KEY,KEY` (repeatable) approves a reviewed independent use only when
all participants in the reported split are named, including the changed key;
malformed lists exit 2 and stale keys fail. An approved split is still never
rewritten automatically, because a citation cannot identify which key it means.
Runs without a split or new flags retain their previous output.

```
python3 scripts/catalog-diff-check.py origin/develop --list-citations
python3 scripts/catalog-diff-check.py origin/develop --allow-labels --rewrite-guide
```

`--list-citations` is read-only and exits 0, including for a split. Its sections are
`guide`, `console tests`, `af-usage.md`, `af-usage.coverage.tsv`, and `Go sources`.
Every row is `path:line<TAB>form<TAB>old<TAB>new<TAB>key`; backslashes, tabs and line
breaks in values are escaped. Shared keys each receive their own rows. These modes,
`--list-pinned`, and `--triples` are mutually exclusive.

Matching is exact and case-sensitive: the complete contents of `「…」`, `**…**`,
quoted strings (`"…"`, `'…'`, backticks, curly double quotes), or a menu segment
separated by ` > ` or `→` must equal the old label. Menu cells end at the next
separator, line end, or punctuation such as parentheses, commas and table bars;
whitespace and formatting delimiters are retained. A bare match in guide prose
must have no Unicode letter, number or underscore immediately before or after it.
A label inside a longer quoted/bold span is excluded, so `保存` does not match
`「保存中」`, `**保存中**`, or `保存中`. Console tests and Go require exact quoted,
bracket or bold matches; the knowledge ledger additionally matches complete TSV
cells. ATX and setext headings are marked `heading`, regardless of their span form.
Fenced guide code is marked `code`. This conservative matching cannot identify
Japanese words embedded in prose or menu cells with explanatory suffixes: search
those manually as well.

`--rewrite-guide` writes only `guide/**/*.ja.md`; `--lang en` instead writes
`guide/**/*.md` excluding `*.ja.md` (including English README pages). It replaces
only exact bracket-quote, bold and menu spans on non-heading lines outside fenced
code. The first column of `guide/ref/settings.ja.md` (English: `settings.md`) is
also rewritable as `table-cell`. Changed `set.tab_*` or `tenant.tab_*` keys produce
`WARN SETTINGS TAB`: update those cells so `scripts/docs-check.py` continues to
pass; its rules are unchanged. Japanese personal tabs also need the matching
`guide/member/12-settings.ja.md` section heading updated manually; table replacement
alone does not satisfy that check. Every edit prints `path:line old -> new`.

The tool checks every target's git status before writing any file. A staged,
unstaged or untracked dirty target is refused with exit 2; review its edits and use
`--force` to allow it. A second run makes no edits. Chained/swapped label mappings
are refused for automatic rewriting to prevent a later run from cascading into a
second replacement; handle those guide edits manually. Symlink targets outside
`guide/` are refused. Existing structure, term and label checks still apply.

After rewriting, the remaining citations are printed in the same TSV sections.
Headings (and their anchors and inbound links, including site `/ja/features/`
links), bare prose, other quoted forms, fenced code, tests, both knowledge files,
and Go sources need manual updates. Any remaining citation exits 1; reviewed
independent matches can use the existing `--exempt-pin KEY@PATH[:LINE]`, with
EXEMPT output and stale-exemption warnings. Exempted spans are also protected from
automatic rewriting. Re-run after the manual edits.

Worked example: when changing `保存` to `保存する`, first change **every** catalogue
key whose old text is `保存`, then run `--list-citations`. After review,
`--allow-labels --rewrite-guide` changes `「保存」` and `**保存**` to the new label,
but leaves `保存中` untouched and reports a heading such as `## 保存` and a test
assertion such as `toBe("保存")`. Update the heading, its links, and that assertion
in the same PR, then rerun the guard and `scripts/docs-check.py`. No catalogue
rewrite is needed to adopt this tooling, and it remains local-only, outside CI.

Automatic rewrites also protect Markdown source contexts: inline code (including
multiple backticks and multiline spans), fenced and indented code, initial YAML
front matter, HTML tags/attributes and comments, URLs, inline link destinations
and titles, and reference-link definitions. Exact citations in these contexts
are still listed as `code` or `metadata`, require manual review or `--exempt-pin`,
and are never automatically rewritten. Fences retain their opener's character
and length; only a same-character closing fence of at least that length, with no
trailing content, closes them. Blockquote and list containers are recognized.
Nested exact citations such as `「**label**」` and `**「label」**` are listed and
rewritten at their inner label range once; overlapping edits are refused.

Label-sync modes require both versions of each compared catalogue file. Added,
untracked, deleted or moved domain files produce an explicit error and exit 2,
instead of a misleading empty citation list. These modes also report the number
of compared files and changed labels on stderr; a zero result distinguishes no
catalogue diff against the ref from a diff with no changed labels.

Heading protection also applies inside blockquote and list containers, including
setext headings: their citation text and anchors remain manual. The settings
`table-cell` exception is limited to a changed `set.tab_*` / `tenant.tab_*` key's
first-column cell beneath a valid `タブ` (Japanese) or `Tab` (English) table header
and separator. Concept/layer tables and non-tab keys retain ordinary manual prose
handling. SPLIT remains always on: a partial shared-label rewrite intentionally
changes the failure output and exit status even without new flags. Byte-identical
legacy output is preserved for runs without a SPLIT violation or new flags.

For setext headings, every line of the preceding Markdown paragraph is manual,
including wrapped headings inside quote/list containers. Container normalization
also applies to reference-link definitions: their destinations, continued
next-line destinations and wrapped titles are protected as metadata. Blank lines
end these blocks, so ordinary citations in surrounding paragraphs remain eligible
for the normal rewrite rules.

### Japanese notation batches (`ja-notation-normalize.py`, phase B1)

`scripts/ja-notation-normalize.py` is a deterministic, local-only planner and editor
for notation in `console/src/lib/i18n/locales/ja/<domain>.ts`. It is not wired into
CI and does not evaluate TypeScript or call a service. This tool's introduction
changes no catalogue values. Terminology choices (sign-in/login, deployment,
terminal, limits, collapse) belong to B2; spelling decisions involving button
behaviour remain manual. The rules follow [Japanese notation conventions](../CONVENTIONS.md#11-japanese-notation-in-ui-text-and-the-guide)
and the [glossary](../../guide/ref/glossary.md).

- **R1:** insert a half-width space at Latin/Japanese and digit/Japanese boundaries,
  e.g. `Gitホスティング` → `Git ホスティング`, `30日後` → `30 日後`.
  Placeholder boundaries are eligible only for the inspected numeric names
  `n`, `count`, `days`, `profiles`, `hosts`, `bytes`, `applied`; `{n}人` becomes
  `{n} 人`. Other names such as `{msg}` and `{name}` are skipped and reported.
  Existing spaces, string ends and Japanese punctuation/brackets
  (`、。「」（）・：`) are never changed. Single-token labels of at most three
  characters are skipped. Latin units, ranges, times, versions and multipliers
  (`30GB`, `30 GB`, `1〜10`, `12:30`, `v1.2`, `3x`, `×1.25`) are protected,
  including boundaries touching those tokens.
- **R2:** `既に` → `すでに`. `無い` → `ない` and `無く` → `なく` require an
  inspected key and its exact original value in `scripts/ja_notation_contexts.json`.
  Each occurrence is printed as `CONTEXT R2 key@offset token | full value`,
  including protected and rejected contexts. The adjective/auxiliary whitelist
  cannot approve a different key or changed sentence. Compound nouns (`無料`,
  `無効`, `無制限`, `無視`, `無理`, `無事`, `無限`, `無駄`, `無数`), noun `無し`
  and verb forms `無くす`/`無くなる` are never converted. Adding a new whitelist
  entry requires grammatical inspection; batch executors must not add approvals.
- **R3:** a plain `Workspace` word in running Japanese text becomes
  `ワークスペース`. Identifiers and adjacent Latin words (possible product names,
  including the catalogue's `Google Workspace` and `Workspace Agent`) are
  protected; the standalone `Workspace` label is skipped. Existing spaces stay.
  Each affected key emits `KEY<TAB>Workspace<TAB>ワークスペース<TAB>N`, accepted
  by `catalog-diff-check.py --allow-terms-file`. Other unexplained glossary or
  Latin-token drift causes the entire value's proposal to be skipped.

All rules preserve code spans, placeholders' contents, complete Trans slots
(`<n>…</n>`/`<n/>`), quoted text (including Japanese bracket quotes), identifiers,
paths, URLs and environment variables. Unbalanced markup is skipped.
Agent-facing `plan.review_prompt_*` and `wi.prompt_*`, notification speech
(including `speech_bare` and other speech variants), `err.*`, `chat.report.*`
and `clean.reason*` are excluded. UI descriptions such as `launch.first_prompt_note`
remain eligible. Valid Unicode surrogate escape pairs are decoded for reports and
matching while their original escape bytes stay intact; unpaired surrogate escapes
are refused before any catalogue or artifact writes. Escaped characters and
boundaries between concatenated literals are skipped when an edit cannot map to unchanged source
syntax. The scanner mirrors the catalogue guard's tokenizer and edits only
literal contents; keys, comments, quote style, escapes, line breaks, entry order
and `en/` stay intact. Unsupported expressions, duplicate keys and symlink
catalogues are refused. Selected dirty catalogue files (staged, unstaged or
untracked) are refused before any edit unless `--force` is explicitly supplied
after review. This flag does not override any notation exclusion.

The report lists every proposed `key | old | new`, skips with reasons, and per-rule
replacement and skip counts per domain. `CANDIDATE (manual only)` lists label
pairs that differ by spaces, `…` or `を`, across domain boundaries; it never
chooses a spelling or changes ellipses. Shared labels are checked across the whole
catalogue: a proposal that would cause SPLIT is skipped with the other keys listed.
Select those domains together only when the batch's scope allows it.

```sh
python3 scripts/ja-notation-normalize.py --all
python3 scripts/ja-notation-normalize.py --domain settings --dry-run \
  --report "$AF_WORK_DIR/settings-plan.txt" \
  --allow-terms-out "$AF_WORK_DIR/settings-plan.tsv"
python3 scripts/ja-notation-normalize.py --domain settings --apply \
  --allow-terms-out "$AF_WORK_DIR/settings-applied.tsv" \
  --report "$AF_WORK_DIR/settings-applied.txt"
# Comma-separated domains and a nonempty subset of R1,R2,R3 are supported:
python3 scripts/ja-notation-normalize.py --domain settings,repos --rules R1,R2
```

Dry-run is the default; `--all` is dry-run only. Report and allowance outputs are
optional during planning. Applying Workspace changes requires
`--allow-terms-out`; without it the tool refuses before writing. Use fresh output
paths: existing artifacts, identical report/allowance paths, catalogue/script
paths and git metadata paths are refused, including a worktree's actual private
and shared Git directories when `.git` is a gitdir file. Artifact creation happens
before catalogue writes, so an artifact write failure leaves the catalogue intact.
Keep the applied allowance file until the PR is reviewed. A second apply with reviewed dirty files and `--force` makes
no additional catalogue edits; it must not overwrite the first allowance file
with an empty plan. In a Managed session where `AF_WORK_DIR` is unset, use
`~/.af-work/<working-copy-directory>/` instead and clean up afterwards.

**Per-domain executor runbook:**

1. Start with a clean catalogue on the assigned branch. Run `--all` for sizing,
   then the selected domain's dry-run with fresh report/allowance paths. Read
   **every proposal, CONTEXT and SKIPPED**. Leave skipped cases unchanged; send
   grammatical/product-name decisions to the reviewer. Do not broaden the rules
   or whitelist and do not perform B2 terminology work.
2. Apply the same domains and rules, emitting a fresh applied allowance file.
   Inspect the diff for value-only changes and verify that a second dry-run has
   zero proposals. An untracked or edited catalogue requires review before
   `--force`; it is not a shortcut around the clean starting point.
3. Run the printed guard command. Keep the allowance argument in every checking
   or rewriting invocation:

   ```sh
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv"
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv" --list-citations
   python3 scripts/catalog-diff-check.py origin/develop --allow-labels \
     --allow-terms-file "$AF_WORK_DIR/settings-applied.tsv" --rewrite-guide
   ```

   The required final guard exit is **0**. Before citation sync, exit 1 for
   PINNED is expected when labels still have citations; it is not permission to
   ignore failures. Resolve all other categories before continuing. The guide
   rewrite can return 1 for remaining manual citations. Update headings and
   inbound anchors, bare prose, tests, knowledge and Go citations in the same
   notation PR. Read each `--list-citations` hit; `--exempt-pin` is only for a
   reviewed independent use, and SPLIT requires all participants or a separately
   reviewed `--allow-split` decision. Also manually search sentence prefixes,
   because the guard cannot find every shortened quote.
4. Rerun the guard to exit 0, `python3 scripts/docs-check.py`, and the relevant
   Console tests **from `console/`** with capped workers. Check the new and
   existing script suites if rules change:
   `python3 -m unittest discover -s scripts -p test_ja_notation_normalize.py` and
   `python3 -m unittest discover -s scripts -p test_catalog_diff_check.py`.
5. The PR body records domains/rules, proposed/changed/skipped counts, inspected
   precision (wrong proposals / all proposals), allowance rows, citation updates
   and manual decisions, exact verification commands and exit codes. Keep labels
   and sentence batches separate when required by the revision plan. Run the
   pre-commit hook, commit, push and open the PR against `develop`.

The initial scratch settings acceptance run inspected all 30 changed values:
0 wrong proposals (100% precision), 36 R1 insertions, 8 R2 replacements and 1 R3
replacement, with 9 skipped occurrences. An earlier 32-value plan had two unsafe
`{msg}` boundary proposals; restricting placeholder spacing to inspected numeric
names removed them. The complete catalogue dry-run took about one second over
23 domains. The scratch guard initially returned 1 with 20 PINNED hits and no
structural/invariant failures; after guide rewriting and manual citation sync,
the final guard returned 0. No acceptance edits are committed to the catalogue.
The suite also removes each protected-span recognizer and selected other exclusions
and proves that their negative controls fail; the real guard fixture fails without
the emitted Workspace allowance and passes with it.
