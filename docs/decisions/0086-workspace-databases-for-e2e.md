# 0086. Postgres and MySQL for e2e tests, one environment per member — pinned servers installed on demand and run as `dev` inside the Workspace, behind a contract a per-member service can take over later

English | [日本語](0086-workspace-databases-for-e2e.ja.md)

- Status: **proposed** (2026-09-16). Nothing is implemented.
- **What was measured, and where.** Both servers were really installed and started in a
  Workspace container on 2026-09-16 — a docker-runtime development deployment, x86_64, a
  10 GiB cgroup limit and 8 CPUs — and every number under "Measured" came out of that run.
  **Nothing was measured on Fargate**, so every claim about EFS, about task-local disk and
  about user namespaces there is named under "Open questions" instead of being decided here.
- The request is one sentence: **let a member run e2e tests against Postgres / MySQL without
  ceremony; not a sidecar, an environment carved out per member.**
- Related: [0044-workspace-sizing.md](0044-workspace-sizing.md) (where `~` lives, and the
  task-local disk this puts a datadir on) / [0045-ec2-persistent-workspace.md](0045-ec2-persistent-workspace.md)
  (the per-membership resources the CP already creates — the shape option B would copy) /
  [0047-tenant-network-restriction.md](0047-tenant-network-restriction.md) (the egress
  allowlist two new upstreams would have to enter) / [0068-debian-13-base.md](0068-debian-13-base.md)
  (the image these three missing shared libraries would be added to) /
  [0071-self-hosted-inference-engines.md](0071-self-hosted-inference-engines.md) (the
  on-demand / idle-stop shape, and why an *engine* is the wrong home for this)

## Background

### What a member hits today

A Workspace has no root, no `sudo`, no Docker and no database server. The operating policy
shipped to every container says so in as many words, and tells the agent to give up:

> **No Docker / Podman**, and no database servers (`psql`, `sqlite3`, `redis-cli` absent).
> Testcontainers, `docker compose` fixtures and "just start a Postgres" do not work — run
> such tests against a service the user provides, or skip them and say plainly that you did.
> — `workspace/notes/environment.md:81`

That paragraph is accurate, and it is the whole problem. "Run it against a service the user
provides" means every member who wants a DB-backed e2e test has to own a database somewhere
else and hand its credentials to a container; "skip them and say so" means the agent stops at
the exact tests that would have caught a migration defect.

### The precedent already in the tree

Three of them, and this ADR is mostly their composition.

1. **On-demand pinned installers.** `workspace/agent/install_tools.go:1` — the lean rootfs
   ships without chromium, Go, the AWS CLI and the ops MCP binaries; each is installed into
   the persistent home on first use, pinned by `/usr/local/share/agent-fleet/versions.json`,
   downloaded to a staging dir and moved into place atomically so a failed download never
   corrupts an existing install. The per-user root is `~/.local/share/agent-fleet`
   (`install_tools.go:29`).
2. **A per-workspace selection the Console writes and every new session re-reads.**
   `workspace/agent/env_toolchains.go:25` holds node / java / go / timezone in
   `~/.config/agent-fleet/toolchains.json`; the CP proxies `GET|PUT /api/env/toolchains` to
   the Agent (`control-plane/routes.go:769`) and the Console edits it in the workspace
   settings. A change takes effect on the next session launch without a Stop → Start.
3. **The maintainer already does this by hand.** `docs/build/10-development.md` §10.4 tells a
   developer to start a Postgres from unpacked binaries on a unix socket, run the three
   migration tests against it, and stop it — because those three tests are the only place
   that catches "added to one dialect but not the other". **This ADR is that harness, made a
   product feature for every member.**

### Where a datadir can live

ADR 0044 decision 3 split `~` by average file size: credentials, `~/repos` and tarball-shaped
caches stay on EFS; `node_modules`, `target`, `.venv` and the build caches are relocated onto
a **task-local disk** (`AF_WS_SCRATCH`, `workspace/af-scratch.sh`), which is fast and **is
wiped when the workspace stops**. A database for tests is exactly the second shape: thousands
of small files, cheap to regenerate, and "gone when the workspace stops" is the semantics a
test fixture wants anyway.

### What the container shares

One member's Workspace is one container, but **several sessions run inside it at once**. That
is what makes "per member" insufficient on its own:

- **Ports are shared.** A TCP port taken by one session's server is taken for all of them.
- **Session names are slot names and get reused** — `workspace/agent/cleanup_ops.go:103`
  exists precisely because per-session side files keyed by name resurface on whatever session
  lands in that slot next.
- The repository's own Postgres tests open with `DROP SCHEMA public CASCADE`. Two sessions
  pointed at one database would delete each other's work with no error anywhere.

## Measured (2026-09-16, in a Workspace container, x86_64)

| | PostgreSQL 16 (Zonky binaries) | MySQL 8.4.6 (official `minimal` tarball) |
|---|---|---|
| Supply | Maven Central jar 14 MB → 59 MB unpacked | `cdn.mysql.com` 63 MB (xz) → **446 MB** unpacked |
| First-time init | `initdb` **2.6 s**, empty datadir 39 MB | `--initialize-insecure` **12.8 s**, empty datadir **200 MB** |
| Start → accepting | **74 ms** | **1.8 s** |
| Resident | 16 MB + 5 children ≈ 45 MB (`shared_buffers=32MB`) | **226 MB** (`innodb_buffer_pool_size=64M`, `performance_schema=0`) |
| Making one more database | `CREATE DATABASE` **0.63 s**, and `TEMPLATE` copies a seeded one | drop / create, then re-run the migrations |
| Architectures | amd64 **and** arm64, majors 16 / 17 / 18 all present | x86_64 only for `minimal`; **arm64 has only the 909 MB full tarball** |

1. **Both run as `dev`, with no root and no capabilities.** Postgres on a unix socket with
   `-h ''`; MySQL with `--skip-networking --socket=…`. A `CREATE TABLE … JSON` /
   `SELECT j->>'$.a'` round trip answered on MySQL, so it is a working server and not just a
   process that stays up.
2. **Three shared libraries the image does not have**: `libaio.so.1`, `libnuma.so.1`,
   `libncurses.so.6` — MySQL's tarball links all three. The run above got past them by pulling
   `libaio1t64`, `libnuma1` and `libncurses6` out of Debian `.deb`s without root and pointing
   `LD_LIBRARY_PATH` at them (~250 KB in total). Postgres needed nothing.
   ⚠️ The version matters: `libncurses6` from unstable wants `GLIBC_2.42` and fails on this
   image; the trixie build (6.5) is the one that works.
3. **No container runtime, still.** `unshare -Ur` succeeds here (unprivileged user namespaces
   are enabled), but `newuidmap` / `newgidmap` do not exist — the image strips every setuid
   bit and asserts the result at build time (`workspace/Dockerfile:642`). Rootless Docker and
   Podman therefore remain impossible, and with them Testcontainers. (docs/log/62 §62.4 said
   the same in 2026-08; this re-measures it rather than citing it.)
4. **Neither upstream is in the default egress allowlist.** `control-plane/egress_policy.go:18`
   admits the git hosts, npm, PyPI, the Go proxy, `.debian.org` and `.amazonaws.com` —
   **not** `cdn.mysql.com`, and **not** `repo1.maven.org`. `.debian.org` being already open is
   what makes the `.deb` route in note 2 work under `enforce` with no allowlist change at all.
5. **MySQL's cost is structural, not tuning.** 226 MB resident is *after* switching
   `performance_schema` off and shrinking the buffer pool; Postgres idles at a fifth of that
   and starts 24× faster. Any design that treats the two engines as interchangeable in size
   will be wrong about memory by a factor of five.
6. The container itself: `memory.max` 10 GiB, 8 CPUs, `AF_WS_SCRATCH` unset (this deployment
   keeps `~` on local disk), so **the EFS and task-local numbers of ADR 0044 were not
   re-measured here**.

## Decisions

### 1. The contract is `af-db` and a URL; **where the server runs is an adapter**

A member and an agent learn exactly three things: `af-db up <engine>`, `af-db url` (a
connection URL for *this session*), `af-db down`. Nothing in that contract says the server is
a process in this container. P0 implements it with a local process (decision 2); a per-member
remote service (rejected below as "not now", not as "never") can be a second adapter behind
the same three verbs, and no project's test setup changes when a deployment switches.

This is the `Runtime` port's shape (`control-plane/internal/runtime/runtime.go:12`): one
interface, four adapters, callers that cannot tell docker from ECS. The reason to fix the
contract *first* is that the expensive part of option B is not the ECS service — it is
discovering that every member's test harness hard-coded a socket path.

### 2. P0 runs the server inside the Workspace, as `dev`, installed on demand

Not a sidecar container, not a shared server. The reasons, in order:

- **It is the only answer that works on every deployment profile.** native, docker, ECS
  Fargate and ECS on EC2 all get the same feature on the same day. A per-member ECS service
  cannot exist under native at all.
- **It costs nothing.** No task, no EFS access point, no Cloud Map name, no hourly bill. What
  it does cost is memory out of the workspace's own quota (`resolveWorkspaceMemBytes`,
  `control-plane/workspace_lifecycle.go:534`), which decision 7 makes visible.
- **It is fast enough to be invisible.** 74 ms (Postgres) against the 1–2 minutes a Fargate
  task takes to pull an image and converge.
- **The installer machinery already exists** and is the one piece of this ADR that is not new
  code but a new table row (decision 5).

### 3. One server per (engine, version) per Workspace; **one database per session**

The server is workspace-wide — a second copy of MySQL is 226 MB for nothing. Isolation between
concurrent sessions is a **database inside it**, created on first `af-db url` in that session
and dropped when the session is deleted.

- Postgres makes this cheap: `CREATE DATABASE … TEMPLATE <seed>` measured at 0.63 s, so
  "a pristine database per session" is affordable and `af-db reset` is the same operation.
- 🔴 **Key the database on the session's identity, not on its name.** Session names are slot
  names and get reused (`workspace/agent/cleanup_ops.go:103`); a database named after the slot
  would be inherited, with its rows, by the next session that lands there — the same defect
  that file already exists to prevent. The drop belongs next to `removeSessionSideFiles`.
- A member who wants one database shared by several sessions asks for it by name
  (`af-db url --db=shared`). The default is not shared.

### 4. The datadir goes on the task-local disk when there is one; home is opt-in

`$AF_WS_SCRATCH/af-db/<instance>` when set, `~/.local/state/af-db/<instance>` otherwise.
Test data that disappears when the workspace stops is the correct default — it is a fixture,
not a document — and it keeps a few thousand small files off EFS, which ADR 0044 measured at
about 14.5 ms per file.

`af-db up --persist` puts the datadir in the home instead, for a member who is building a
dataset by hand. **On a Fargate deployment that home is EFS, i.e. NFS**, and an InnoDB or
Postgres datadir on NFS is a performance and correctness question nobody here has measured —
so `--persist` prints what it is doing and the open questions below say what to measure before
it is recommended for MySQL.

### 5. Supply: downloaded at run time, pinned in `versions.json`, verified by SHA-256

New keys next to the existing ones (`workspace/Dockerfile:503`): `postgres`, `mysql`, and a
`_sha256` for each artefact. The installers follow the `install-jdk` idiom exactly — staging
dir, atomic rename — so a half-finished download can never present itself as an install.

- **Postgres** comes from the Zonky `embedded-postgres-binaries` jars on Maven Central: 14 MB,
  both architectures, majors 16 / 17 / 18, and it needs nothing the image lacks. Offer 16, 17
  and 18; default 17.
- **MySQL** comes from the official `minimal` tarball; default 8.4 (LTS). x86_64 only —
  arm64 is open question 3.
- **Not baked into the image**: a lean rootfs exists to *not* carry 450 MB of tools nobody
  asked for, and downloading at run time also keeps a GPL-licensed server out of an image the
  project distributes.
- **Three things do get baked**: `libaio1t64`, `libnuma1`, `libncurses6` (~250 KB). Scavenging
  `.deb`s at run time works (measured) but makes every member's install depend on a Debian
  pool layout and on picking the suite that matches the image's glibc — a footgun measured in
  note 2, not a supply chain.

### 6. Listen on a unix socket **and** on `127.0.0.1`

The socket is the default because ports are shared between a member's sessions and a fixed
port is a collision waiting for the second session. But **JDBC cannot speak unix sockets** —
neither the Postgres nor the MySQL driver does without an extra native library — so a
Java-shaped e2e test is unreachable through a socket alone, and that is a large share of the
e2e tests this ADR exists for.

So the Agent binds both, allocates the TCP port itself, records it in the instance registry
under `~/.config/agent-fleet/`, and `af-db url --tcp` prints it. Binding is `127.0.0.1` only:
the container is the member's, but nothing about this feature should be reachable from outside
it.

### 7. Idle-stop by default, and the memory is shown, never hidden

An idle server is 45 MB (Postgres) or 226 MB (MySQL) taken from the same cgroup as the
member's builds, and a JVM build next to a running MySQL is how a workspace earns exit 137.

- The Agent stops an instance that has had no connection for 30 minutes; `af-db up` after that
  is a 74 ms / 1.8 s restart, not a re-install.
- The Console card shows resident size next to the state, so "is it safe to leave this on" has
  an answer on screen rather than in a runbook.
- The operating policy note that today says "just start a Postgres does not work" is replaced
  by one that says how, **and** says to stop it before a heavy build.

### 8. No new MCP tool

Every MCP tool's description is a fixed token cost in **every** session of every agent,
whether or not that session will ever touch a database. The surface is a CLI (`af-db`), two
environment variables the Agent injects into sessions, and one paragraph in the operating
policy the agents already read. The agent-facing win is the paragraph: today it tells them to
give up, which they correctly obey.

`AF_DB_URL_POSTGRES` / `AF_DB_URL_MYSQL` are injected when an instance is running. **`DATABASE_URL`
is never set implicitly** — it is the variable a member's own project is most likely to read,
and silently pointing an application at a test database is a defect this feature would be
blamed for. `eval "$(af-db env)"` sets it, in the shell that asked.

### 9. The Console surface is a card in the workspace settings, proxied like toolchains

`GET|PUT /api/env/databases` proxied to the Agent exactly as `/api/env/toolchains` is
(`control-plane/routes.go:769`), drawn as a card in the Env tab: engine, version, state,
resident size, the connection URL with a copy button, Start / Stop / Reset. No new CP concept,
no new stack, no new IAM.

### 10. The Agent owns the process, not the session

The server is started detached by the Agent, survives the session that asked for it, and is
stopped by the Agent on idle or on container stop. A session ending must not kill a server its
sibling sessions are using, and no member should ever have to reason about which terminal the
database is "in".

## Rejected

- **A sidecar container next to the Workspace.** The user's own constraint, and the
  measurements support it: it is resident whether or not it is used, it is one engine at one
  version chosen by the operator, it dies with the workspace, and it exists only on the
  deployment profiles that have a task definition — native gets nothing.
- **One shared server with a database and a role per member.** Cheapest to run, and genuinely
  "carved out per member" in the DBA sense, but: no superuser, so a member cannot test an
  extension, a collation or a version difference; one member's runaway query is every member's
  problem; and a shared server has to exist somewhere, which native and docker deployments
  would have to be told to provide. Kept in mind as a possible third adapter for a deployment
  that explicitly wants it.
- **A per-member DB service the CP provisions (option B), now.** Technically unremarkable —
  it is structurally the same thing ADR 0045 already does per membership (an ECS service, an
  EFS access point, an SSM secret, a Service Connect name, a reaper that stops it) — and it is
  the right answer for a member who needs a persistent, large, or shared-between-workspaces
  database. It is rejected **for now** on cost and sequence: ≈$0.013/h per member for a
  0.25 vCPU task (≈$9/month if nobody stops it), a start measured in minutes rather than
  milliseconds, ECS-only, plus cost attribution (ADR 0048), credential rotation (ADR 0065) and
  a CFN stack. Decision 1 keeps the door open at the price of one interface.
- **RDS / Aurora Serverless for members' tests.** A separate cluster is option B with a higher
  floor and less isolation; the CP's own metadata cluster is not a place to give test code a
  role, at any privilege.
- **Rootless Docker / Podman in the Workspace, and with them Testcontainers.** Measured
  impossible again on 2026-09-16 (note 3): user namespaces are available, `newuidmap` is not,
  and restoring it means putting setuid binaries back into an image that asserts it has none.
  A per-member Docker *host* (a box the CP buys, as ADR 0077 buys GPU boxes, exporting
  `DOCKER_HOST`) is the only shape that would unlock Testcontainers; it is a separate ADR with
  an EC2 bill attached, not a decision to smuggle in here.

## Open questions (measure before deciding)

1. **Do unprivileged user namespaces work in a Fargate task?** They work in this
   docker-runtime container; Fargate is a different runtime and the answer is unknown. It does
   not affect P0 — it decides whether the per-member Docker host above ever has a cheap
   alternative. Probe: `unshare -Ur echo ok` in a Fargate workspace.
2. **A datadir on EFS: how slow, and is it safe?** Decides whether `--persist` (decision 4) is
   recommended, discouraged, or refused for MySQL. Measure `initdb` + a 10k-row insert on both
   ECS and docker profiles.
3. **arm64 MySQL.** No `minimal` build exists; the full tarball is 909 MB compressed. Two
   candidates: prune the full tarball after unpack (`bin/mysqld`, `share/`, `lib/plugin` —
   `bin/` alone is 222 MB of which `mysqld` is 78 MB), or take MariaDB from Debian
   (`.debian.org` is already allowlisted; MariaDB publishes no arm64 bintar of its own). Until
   one is measured, P1 ships MySQL on x86_64 and says so on the Console card rather than
   offering a version that will fail to install.
4. **Two new upstreams in the default egress allowlist** (`cdn.mysql.com`, `repo1.maven.org`)
   — or the `.deb` route for both engines, which needs none. Note that Maven Central is
   absent today, which is its own gap for a member building a JVM project under `enforce`.
5. **Does the default workspace memory need to rise?** A 2 GiB workspace running MySQL has
   1.8 GiB for everything else. The lever is ADR 0044's sizing; the data needed is how many
   members actually turn MySQL on.

## Phases

- **P0 — Postgres, CLI only.** `af-db up|url|reset|down|status` for Postgres 16/17/18; the
  pinned installer; socket + `127.0.0.1`; one database per session with the identity key of
  decision 3; datadir on the task-local disk; idle stop. Docs: the operating policy note
  (`workspace/notes/environment.md`), a member-guide procedure, and `docs/build/10-development.md`
  §10.4 rewritten to use `af-db` instead of the hand-rolled harness. **Done means** the
  repository's own `TestPostgres | TestSchemaDialectParity` run green against an `af-db url`
  in a fresh workspace, from a `go test` line with no local setup.
- **P1 — MySQL and the Console.** The three shared libraries in the image; the MySQL installer
  (x86_64, open question 3 for arm64); the Env-tab card of decision 9 with resident size; the
  memory guidance in the policy note.
- **P2 — decided by demand, not by this ADR.** A second adapter behind decision 1: either the
  per-member service (option B) for deployments that need persistence, or a shared server with
  per-member databases. Additional engines (Redis, MongoDB) are table rows in the same
  installer if they are asked for.

## Sources checked (2026-09-16, this repository's code)

- `workspace/notes/environment.md:81` — the policy paragraph this feature has to replace.
- `workspace/agent/install_tools.go:1`, `:29` — the on-demand pinned installer idiom and the
  per-user install root.
- `workspace/Dockerfile:503` — `versions.json` is generated in the image build;
  `workspace/Dockerfile:642` — every setuid bit is stripped and the absence asserted.
- `workspace/agent/env_toolchains.go:25`, `:29` — the per-workspace selection file the
  database registry copies; `control-plane/routes.go:769` — how the Console reaches it.
- `workspace/agent/cleanup_ops.go:103` — per-session side files, and the slot-name reuse trap
  decision 3 inherits.
- `control-plane/egress_policy.go:18` — the default allowlist, and the two absent upstreams.
- `control-plane/internal/runtime/runtime.go:12` — the port/adapter shape decision 1 copies.
- `control-plane/workspace_lifecycle.go:534` — the workspace memory quota an instance spends.
- `workspace/af-scratch.sh`, `workspace/entrypoint.sh:245` — `AF_WS_SCRATCH` is task-local and
  wiped on stop.
- `docs/build/10-development.md` §10.4 — the hand-rolled Postgres harness this productises.

## Review (2026-09-17, before P0)

In the manner of 0071's and 0076's reviews: does each decision follow from its evidence, and
does the code say what the draft says it says? Every `file:line` under "Sources checked" was
re-read with `sed -n`, both servers were started again in the same container (docker-runtime
deployment, x86_64, 10 GiB cgroup, 8 CPUs), and open question 3 was measured. The verdict
first: **P0 may start, but decision 3 has to be rewritten before it is coded** — its premise
is not what the code does, and the per-session key it relies on does not exist on the managed
route. Decisions 1, 2, 5–10 stand; two of them gain a P0 item each (a client, a shim). The
2026-09-16 numbers all reproduced; two moved in the ADR's favour.

### Premises against the code

- **All citations resolve.** `runtime.go:12` is the comment line above the interface (`:13`);
  "docs/log/62 §62.4" is §62.4.1 (`docs/log/62-ecs-start-latency.md:198`). "Four adapters" is
  four profiles in `NewFactory` (`local|docker`, `ecs`, `ecs-ec2`, `native|wsl`) over three
  struct types — accurate as stated.
- 🔴 **Decision 3's premise is false: session names are not slot names, and they are not
  reused.** Since 2026-07-03 (commit `4ae4424a`) a session name is a random slug — `"s"` plus
  six base32 characters, about 30 bits — and `allocSessionName`
  (`workspace/agent/internal/sessionx/session_name.go:51`) refuses any slug that has a meta,
  a live tmux session, *or* a jsonl on disk; the comment above it calls the slug "the session's
  IMMUTABLE identity". The comment at `cleanup_ops.go:103` ("slot names and get REUSED") was
  written on 2026-08-19, after that change, and describes the earlier `slotNN` scheme. There is
  no second identity to key on: `session.UUID(dir, name)` is derived from the name
  (`internal/session/uuid.go:19`), and `session.Meta` has no id field. So "key the database on
  identity, not on name" has nothing to key on and nothing to protect against — **the name is
  the identity.** What the ADR should say instead: key the database on the slug, and do not
  hook the drop into `removeSessionSideFiles` — that is one of five places a meta is removed
  (`cleanup_ops.go:80`, `:98`, `internal/gitx/git.go:1701`,
  `internal/sessionx/session_handlers.go:161`, `:1169`). Have `af-db` **reconcile** instead: on
  every `up` / `url` / `status`, drop databases whose slug no longer has a meta. One code path,
  and it also cleans up after a stop that never ran a drop.
- 🔴 **"A connection URL for *this session*" cannot be answered on the managed route.** The
  only place `AF_SESSION_NAME` enters a process environment is the tmux launch
  (`internal/sessionx/session_tmux.go:40`). A managed session runs inside one shared daemon
  per kind — `internal/agents/codex/driver.go:88` says so and measured it, and opencode's
  `EngineEnv` exists because that daemon's environment is workspace-scoped — so a shell an
  agent runs from a managed session carries no session name at all. The codex precedent falls
  back to the working directory. Hence the practical default key is the **working copy**
  (`Meta.Dir`), not the session: `af-db url` resolves the caller from `AF_SESSION_NAME` when it
  is set, otherwise from cwd → the working copy. Two sessions sharing one working copy already
  share its files and branch; sharing its test database is the same trade, and `--db=<name>`
  remains for the explicit case. Decision 8's "two environment variables the Agent injects"
  has the same limit — environment is fixed at launch, and only for tmux sessions, so a server
  started later is invisible to a running session by env. `af-db url` / `eval "$(af-db env)"`
  is the path that always works; the injected variables are a convenience for tmux sessions.
- **Decision 5's supply has a gap the draft does not name: Zonky ships no client.** The
  retained `pg.jar` holds one `postgres-linux-x86_64.txz`, and the unpacked `dist/bin` is
  `initdb`, `pg_ctl`, `postgres` — nothing else. Two consequences. (a) The Agent needs a
  Postgres wire client for `CREATE DATABASE`, `reset`, the reconcile above and the idle check
  (`pg_stat_activity`); `workspace/agent/go.mod` carries none, `pgx/v5` is already in the CP's
  module and is pure Go. (b) The member gets a server and no `psql`, which for "without
  ceremony" is half the feature. The `.deb` route (`.debian.org` is allowlisted, and note 2
  measured it working) supplies `postgresql-client-17` and `libpq5`; name it in P0. MySQL's
  `minimal` tarball ships `mysql`, `mysqladmin`, `mysqldump` and 26 more under `bin/`, so
  shelling out is fine there.
- **Decision 9 is one line on each side plus nothing new.** `proxy.rest`
  (`control-plane/proxy.go:148`) forwards `/api/<x>` to the Agent's `/<x>` generically and
  counts PUT as workspace activity, which is right for Start / Stop. What it needs is the route
  registered in **both** `control-plane/routes.go` (next to `:769`) and
  `workspace/agent/routes.go` (next to `:355`): the CP relays by explicit allowlist, not
  catch-all, and a missing CP line is a silent 404 in the Console.
- **Decision 5's pins and the shim.** `versions.json` is a flat map the Dockerfile writes from
  ARGs (`Dockerfile:500`), with per-architecture sha keys chosen at build time (`kiro_sha256`,
  `install_kiro.go:61`). Three Postgres majors × two architectures is six sha ARGs; the
  cheaper shape is to pin the default major (`postgres` + `postgres_sha256`) and verify the
  others against Maven Central's `.sha256` sidecar, which exists (HTTP 200 for 17.6.0) — the
  `install-go` precedent (`install_tools.go:300`) already fetches its sum from the origin.
  Zonky's arm64 line currently carries 16.15 / 17.11 / 18.6, so "16 / 17 / 18, both
  architectures" holds. And one trap: `workspace-agent <unknown-subcommand>` **boots the
  Agent** (`main.go` is a chain of `os.Args[1] ==` tests with no default), so `af-db` must be
  both a real `/usr/local/bin/af-db` shim and a dispatch line in `main.go`.
  🔴 2026-09-17: the trap is closed. The branches are one table in `cli.go`; an argument it does
  not name prints usage and exits 2, `--version` / `--help` are supported, and only "no
  arguments" and `serve` boot the Agent. `af-db` is still a shim plus a table row. A second
  Agent is harmless too — `serve` now takes the listening socket before any boot side effect.
- **Decision 4 describes one profile as if it were all of them.** `AF_WS_SCRATCH` is set only
  by the ECS adapters (`entrypoint.sh:246`), and the entrypoint relocates only when the disk is
  30 GiB or more (`AF_WS_SCRATCH_MIN_GB`, `entrypoint.sh:263`) — a default Fargate deployment
  has 20 GiB and skips (ADR 0044 decision 5). "When set" and "when the entrypoint relocates"
  are different tests; a 39 MB / 200 MB datadir is not the multi-GiB cache the size gate exists
  for, so say it uses `$AF_WS_SCRATCH` whenever the variable is set. On docker and native the
  default lands in the home and **survives a stop**, so "gone when the workspace stops" is
  the ECS semantics only; `--persist` therefore means something on ECS alone.

### Measured again (2026-09-17, same container)

- **Postgres**: `pg_ctl -w start` 118 ms wall (the log shows listening → ready in 8 ms);
  resident 47 MB = postmaster 17.6 MB + five children. `CREATE DATABASE … TEMPLATE` **57 ms**
  first, **35 ms** second; `DROP DATABASE` 19–86 ms (fsync=off, as §10.4's harness). The
  draft's 0.63 s is an upper bound, not the cost.
- **MySQL** from `tar xJf`: dist 446 MB, `--initialize-insecure` 12.9 s, start → ping
  1.85 s, VmRSS 226 MB (HWM 238 MB), datadir 200 MB, the JSON round trip answered; SIGTERM
  shut it down cleanly in about 1 s. All six numbers reproduce.
- 🔴 **A hazard the retained log had recorded and the draft does not mention.** The previous
  run's `mysqld.log` was 105 MB: 904,817 lines of `[ERROR] Unable to open
  './#innodb_redo/#ib_redo5'` in 6 min 43 s — a datadir removed under a running mysqld.
  mysqld does not exit; it spins at ~2,200 log lines a second. `af-db down` / `reset` / the
  idle stop must stop the process **before** touching the datadir, and `--log-error` must never
  point at EFS. Decision 10 (the Agent owns the process) is what makes that ordering
  enforceable; write the ordering down.

### Open question 3, measured — it decides P1 differently than the draft expected

- `mysql-8.4.6-linux-glibc2.28-aarch64.tar.xz` is **909,017,708 bytes** (under `archives/`;
  the `Downloads/` path 404s, and no `-minimal` exists for aarch64 at either), **1,742 MB
  unpacked**, 471 members; `bin/mysqld` alone is **514 MB**.
- Pruning to the exact member set of the x86_64 `minimal` tarball (440 members) leaves
  **1,245 MB, not 446** — the full tarball is unstripped. `readelf -S` on the arm64 `mysqld`:
  seven `.debug_*` sections, 451 MB of non-allocated sections of which 363 MB is debug; the
  allocated sections — what `strip` keeps — sum to **69 MB**. Cross-check: the x86_64 full
  tarball's `mysqld` is 516 MB against `minimal`'s 78 MB, so `minimal` *is* the stripped full.
- `strip` has to run **on the arm64 workspace itself**: this host's binutils 2.44 refuses
  AArch64 ("Unable to recognise the format of the input file"). The image ships binutils with
  gcc, so the arm64 install path is: download 909 MB, extract `bin/mysqld`, `bin/mysql`,
  `lib/private/*.so`, `lib/plugin/*.so`, `share/`, strip in place, expect on the order of
  200 MB on disk. The download, not the disk, is the cost.
- The arm64 `mysqld`'s `NEEDED` list outside libc / libstdc++ / OpenSSL is exactly
  `libaio.so.1` and `libnuma.so.1`; `libncurses.so.6` is needed by the `mysql` client only.
  Decision 5's "three things get baked" is right for the client, two for the server.
- **MariaDB from Debian trixie** is the other candidate: `mariadb-server-core_11.8.8-0+deb13u1_arm64.deb`
  is 7.1 MB (installed 46 MB, `mariadbd` 27 MB) and `mariadb-client-core` is 0.9 MB. Its
  `Depends` adds `liburing2`, which the image lacks, on top of `libaio1t64` / `libnuma1`;
  `libpcre2-8`, `libssl3` and `libsystemd0` are present. 1/130 of the download for the same
  wire protocol, but it is not MySQL 8.4 — JSON, `CHECK`, window functions differ at the edges,
  and a member who asked for "MySQL" is not served. Recommendation: MySQL on both architectures
  via the strip path; MariaDB only if a member asks for it by name. Either way, the "arm64 says
  so on the Console card" fallback in open question 3 is no longer needed.

### The completion criterion

- **P0's "Done means" omits three things it needs to be checkable.** The variable is
  `AF_TEST_DATABASE_URL` (`store_postgres_test.go:19`); the socket URL shape is
  `postgres://postgres@/postgres?host=<sockdir>&sslmode=disable` (§10.4); and the
  `-run 'TestPostgres|TestSchemaDialectParity'` regex matches **four** tests, of which
  `TestPostgresPasswordRotation` skips under `--auth=trust` (`store_postgres_rotation_test.go:91`).
  Rehearsed today against the retained server: `TestPostgresDeleteCascade` 0.34 s,
  `TestPostgresStore` 0.66 s, `TestSchemaDialectParity` 0.55 s — PASS; rotation — SKIP. So
  `af-db` should `initdb --auth=scram-sha-256` with a generated password carried in the URL
  (this also keeps decision 6's `127.0.0.1` listener from being a trust superuser port), and the
  criterion reads: in `control-plane/`,
  `AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./...`
  shows **4 PASS, 0 SKIP**. `-count=1` because a cached `ok` proves nothing.
- 🔴 **MySQL has no test in this repository.** `TestSchemaDialectParity` compares SQLite with
  Postgres (`store_schema_parity_test.go`), and nothing under `control-plane/` speaks MySQL.
  P1's MySQL lane therefore has no in-repo "done"; the phase should say what counts — a member
  project's MySQL suite, or a `go-sql-driver/mysql` smoke added under `e2e/`. The measurements
  stand; the acceptance does not exist yet.

### The shape of the choice

- **Decisions 1 and 2 are the shortest path to "per member, not a sidecar".** What the author
  weighed privately — handiness first, cost and profile breadth as the tie-breakers — is exactly
  what today's numbers support: 118 ms, 47 MB, no infrastructure, and the same feature on
  native, docker and both ECS profiles on the same day. Nothing found today weakens decision 2.
  What weakens "handy" is the missing client above; it is a P0 item, not a redesign.
- **Rejected options hold, one for the wrong reason.** "A shared server with a role per
  member" is rejected for "no superuser", which is wrong as stated — a `CREATEDB` role and a
  database per member cover extensions and collations in-database; only the version choice and
  `ALTER SYSTEM` need superuser. The reason that does stand is the one the draft lists second:
  such a server has to exist somewhere, and native / docker deployments get it for free from
  nobody. Keep the rejection, fix the wording. The sidecar rejection can add: it cannot be
  idle-stopped apart from the workspace. Option B undersells one cost: a per-member ECS service
  is a second service per member, doubling ADR 0045's task count.

### Open questions after this review

- **OQ3 is answered** above: P1 may ship MySQL on arm64 via the strip path; MariaDB is a
  different offer, not a fallback.
- **OQ4 is sharpened**: the `.deb` route covers the client and the three libraries, not the
  servers. Whether Debian's `postgresql-17` server package runs from a home-relocated `.deb`
  on this image was not measured; until it is, the two upstreams stay in the question.
- OQ1, OQ2 and OQ5 cannot be measured on this deployment; unchanged. On OQ5 note that
  `memFloorBytes` is 256 MiB (`workspace_lifecycle.go:415`) — a floor-sized workspace cannot
  run MySQL at all, and `af-db up mysql` should say so rather than earn exit 137.

### Sources checked (2026-09-17, this repository's code)

- `workspace/agent/internal/sessionx/session_name.go:31`, `:51`, `:69` — random immutable slugs
  and the three refusals; `internal/session/uuid.go:19` — the UUID is a function of the name.
- `workspace/agent/internal/sessionx/session_tmux.go:40` — the one place `AF_SESSION_NAME` is
  injected; `internal/agents/codex/driver.go:88` — why the managed route cannot carry it.
- `workspace/agent/cleanup_ops.go:80`, `:98`, `internal/gitx/git.go:1701`,
  `internal/sessionx/session_handlers.go:161`, `:1169` — the five meta-removal paths.
- `control-plane/proxy.go:148` — the generic relay; `workspace/agent/routes.go:355` — the
  Agent side of `/env/toolchains`.
- `workspace/agent/main.go:56`–`:100` — subcommand dispatch with no default;
  `install_kiro.go:61` — per-architecture sha in `versions.json`; `install_tools.go:300` — a sum
  fetched from the origin.
- `workspace/entrypoint.sh:246`, `:263` — `AF_WS_SCRATCH` is ECS-only and gated at 30 GiB.
- `control-plane/internal/store/store_postgres_test.go:19`,
  `store_schema_parity_test.go:24`, `store_postgres_rotation_test.go:91` — the variable, the
  dialect pair, and the trust-auth skip.
- `control-plane/workspace_lifecycle.go:415` — `memFloorBytes`.
- `~/.local/share/af-pgtest` (Zonky 17 dist + data) and `~/.local/share/af-dbtest/my.tar.xz`
  — the retained artefacts the re-measurement used; upstream sizes by `curl -I` against
  `cdn.mysql.com`, `repo1.maven.org` and `deb.debian.org` on 2026-09-17.

## Decisions overridden, decisions kept (2026-09-17, after the review, before P0)

The author accepted the review. The decisions above stay as written; where one is named here,
this section replaces it. The contract table at the end is fixed so that three lanes (supply,
runtime, documentation) can be built by different sessions against the same words.

- **Decision 3 → 3′. One server per (engine, major) per Workspace; one database per
  *working copy*.** The key is the working copy directory (`Meta.Dir`, the git toplevel), not
  the session — the slug is already immutable, and the managed route cannot name its session.
  `af-db` resolves the caller as: `AF_SESSION_NAME` set → that session's `Dir`; otherwise the
  git toplevel of cwd; otherwise cwd. Database name: `af_` + the directory's basename
  sanitised (lower-case, `[^a-z0-9]` → `_`, at most 40 characters) + `_` + the first six hex
  of `sha256(dir)`. The registry records name → dir. **Reconcile** on every `up` / `url` /
  `status`: drop every registered database whose recorded directory no longer exists on disk.
  `--db=<name>` names a shared database explicitly; reconcile never drops those. No hook in
  any of the five session-deletion paths.
- **Decision 4 → 4′.** `$AF_WS_SCRATCH/af-db/` whenever the variable is set, regardless of the
  entrypoint's 30 GiB relocation gate; otherwise `~/.local/state/af-db/`. On docker and native
  the default persists across stops — "gone when the workspace stops" is the ECS behaviour.
  `--persist` forces the home path and turns `fsync` back on.
- **Decision 8 → 8′.** `af-db url` and `eval "$(af-db env)"` are the contract. `AF_DB_URL_POSTGRES`
  is injected at tmux launch only, and only when the instance is running and the working
  copy's database already exists; managed sessions get nothing by environment. `DATABASE_URL`
  is set only by `af-db env`.
- **Decision 5 gains three P0 items.** (a) `workspace-agent install-pg-client`: `postgresql-client-<major>`
  and `libpq5` resolved from the Debian trixie `Packages` index for the build architecture —
  version and sha256 from the index, never a pinned filename Debian retires — unpacked under
  `~/.local/share/agent-fleet/pg-client/`, with `~/.local/bin/{psql,pg_dump,pg_restore}` wrappers
  that set `LD_LIBRARY_PATH`. (b) The Agent takes `github.com/jackc/pgx/v5`. (c) A real
  `/usr/local/bin/af-db` shim and an `os.Args[1] == "af-db"` dispatch in `main.go`.
- **Decision 6 gains authentication.** `initdb --auth=scram-sha-256 --auth-local=scram-sha-256
  -U postgres`, password generated at `initdb` and kept mode 0600 in
  `~/.config/agent-fleet/af-db/postgres-<major>.pass`. The `127.0.0.1` listener is therefore
  not a trust superuser port.
- **Decision 7 is kept, with its mechanism named.** The Agent's loop, every 60 s:
  `SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend'`; zero for
  30 consecutive minutes → `pg_ctl stop -m fast`. `lastUsedAt` is also bumped by every
  `af-db url`.
- **Decision 10 gains the ordering.** stop → wait for the postmaster pid to be gone → only then
  touch the datadir. The server log is `<root>/postgres-<major>.log` via `pg_ctl -l`.
- **P0's completion criterion** is the review's: in `control-plane/`,
  `AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./...`
  shows 4 PASS, 0 SKIP, in a workspace that has never run `af-db` before.
- **Open question 3 is closed** by the review: P1 ships MySQL on arm64 via `strip` on the
  arm64 host; MariaDB is a separate offer, not a fallback.

### The P0 contract

| Item | Value |
|---|---|
| Install root | `~/.local/share/agent-fleet/postgres/<major>/{bin,lib,share}`; `workspace-agent install-postgres <major>`, `major` ∈ {16, 17, 18}, default 17. `AF_DB_POSTGRES_ROOT=<dir>` overrides the root for tests (the retained `~/.local/share/af-pgtest/dist` has the same shape). |
| Pins | `versions.json`: `postgres` = the Zonky version of the default major (e.g. `17.11.0`), `postgres_sha256` = its jar's sha for the build architecture (the `kiro_sha256` pattern). Other majors: the latest Zonky release of that major, verified against Maven Central's `.sha256` sidecar. Jar → `postgres-linux-<arch>.txz` → staging dir → atomic rename. |
| Shim | `/usr/local/bin/af-db` = `exec workspace-agent af-db "$@"` (baked in `workspace/Dockerfile`, next to `af-scratch`). |
| Registry | `~/.config/agent-fleet/af-db/instances.json`, read-modify-write through `fstore` under `~/.config/agent-fleet/af-db/lock`. One instance = `{engine, major, root, datadir, sockdir, port, pid, startedAt, lastUsedAt, persist, databases: {name: dir}}`. |
| Datadir | `<root>/postgres-<major>/data`, `<root>` = `$AF_WS_SCRATCH/af-db` or `~/.local/state/af-db`. |
| Socket | `~/.local/state/af-db/run/postgres-<major>/` (short path; the file is `.s.PGSQL.<port>`). |
| Port | The Agent binds `127.0.0.1:0`, releases it, passes it as `-p`, records it. |
| Server flags | `-k <sockdir> -h 127.0.0.1 -p <port> -c shared_buffers=32MB -c max_connections=50 -c fsync=off` (`fsync=on` under `--persist`). |
| URL (default) | `postgres://postgres:<pw>@/<db>?host=<sockdir>&sslmode=disable` |
| URL (`--tcp`) | `postgres://postgres:<pw>@127.0.0.1:<port>/<db>?sslmode=disable` |
| Verbs | `af-db up [postgres] [--major N] [--persist]` · `af-db url [--db=NAME] [--tcp]` (installs if missing, starts if stopped, creates the database if absent) · `af-db env [--db=NAME] [--tcp]` (prints `export AF_DB_URL_POSTGRES=…` and `export DATABASE_URL=…`) · `af-db reset [--db=NAME]` (DROP + CREATE) · `af-db down [--purge]` (stop; `--purge` removes the datadir after the pid is gone) · `af-db status [--json]` |
| Exit codes | 0 ok · 2 usage · 3 install failed (message names the URL and both shas) · 4 server failed to start (message names the log path) · 5 not running (`reset` only). |
| Code | `workspace/agent/internal/afdb/` holds the CLI verbs and the Agent's idle/reconcile loop; `workspace/agent/install_postgres.go` and `install_pg_client.go` follow `install_kiro.go`. |

## P0 accepted (2026-09-17)

Three lanes were built by three sessions against the contract table, each reviewed by a
fourth session before merge (the reviewer sent its findings to the author by peer message and
re-ran the acceptance after the fixes):

| Lane | Branch | Findings sent / fixed | Merged as |
|---|---|---|---|
| L3 documentation (`workspace/notes/environment.md`, `guide/member/03-code`, `docs/build/10-development` §10.4, plus `AGENTS.md` and `workspace/workspace-notes.md` whose old "no database, skip them" lines contradicted the new note) | `temp/s6arm5b` | 11 / 11 | `a39c4487` |
| L1 supply (`install-postgres`, `install-pg-client`, `Dockerfile` pins and the `af-db` shim) | `temp/s66bqob` | 7 / 7 | `3ad2ce88` |
| L2 runtime (`internal/afdb`, the `af-db` verbs, the idle loop, `session_tmux.go` injection, pgx) | `temp/sawbl7m` | 13 / 13 | `d3b6f3ce` |

**The completion criterion ran green from a HOME that had never seen `af-db`** (this
container, x86_64, the merged branch): `af-db up` installed Zonky 17.11.0 from Maven Central,
ran `initdb` with scram and started the server in **8.7 s** end to end; `af-db url` returned a
socket URL for the working copy's database (`af_agent_fleet_wip_szkxzgu_9af42b`); in
`control-plane/`, `AF_TEST_DATABASE_URL="$(af-db url)" go test -count=1 -run 'TestPostgres|TestSchemaDialectParity' ./internal/store/`
→ **4 PASS, 0 SKIP** (`TestPostgresPasswordRotation` included, because the server is scram, not
trust); `af-db status --json` carried no password; `af-db down --purge` stopped the server and
removed the datadir; no `postgres` process remained. `gofmt`, `go vet` and
`go test -count=1 ./...` are clean on the merged `workspace/agent`.

### Contract corrections found by the reviews (the table above is superseded on these rows)

- **URL (default)**: `postgres://postgres:<pw>@/<db>?host=<sockdir>&port=<port>&sslmode=disable`.
  pgx derives the socket file name `.s.PGSQL.<port>` from the port, so with an allocated port
  `port=` is mandatory on the socket URL too.
- **Registry**: not `fstore` — `fstore` has no lock and forbids read-modify-write. The registry
  is written tmp + rename under a dedicated flock (`~/.config/agent-fleet/af-db/lock`); the
  start path is serialised by a second flock per (engine, major)
  (`postgres-<major>.start.lock`), because two `af-db url` from a stopped state raced to
  `initdb` and the second one exited 4 before the lock existed. `install-postgres` holds its own
  per-major flock for the same reason.
- **Log**: `<root>/postgres-<major>.log` where `<root>` is the *state* root
  (`$AF_WS_SCRATCH/af-db` or `~/.local/state/af-db`), not the install root.
- **`--major N`** is accepted by every verb, default 17.
- **`install-pg-client`**: Debian trixie `main` carries `postgresql-client-17` only; a request
  for 16 or 18 is served with 17 and says so on stderr (PGDG would have all three, but only
  `.debian.org` is in the default egress allowlist). The wrapper execs
  `/usr/lib/postgresql/17/bin/psql` directly, not Debian's `pg_wrapper`, so
  `postgresql-client-common` is not installed.
- **Install time**: 1.6–2.1 s for any Postgres major (15 MB jar → 60 MB tree) and 1.5–2.0 s for
  the client, measured three times each; the guide's "a few minutes" is conservative.
- **The `versions.json` pin path is untested until the image is rebuilt**: this container's
  `versions.json` predates the `postgres` key, so the acceptance went through the
  Maven-metadata route (which chose the same 17.11.0). The pinned route is exercised by the
  unit tests only.
- **Idle stop** counts `client backend` rows excluding `pg_backend_pid()`; the first version
  counted its own probe and would never have stopped anything.
- **`down --purge`** refuses to remove a datadir when the postmaster pid is unknown and
  `pg_ctl stop` failed — the ordering of decision 10, made concrete.

What P0 does not do, on purpose: no Console card (P1, decision 9), no MySQL (P1), no
`AF_DB_URL_POSTGRES` on managed sessions (8′). The next thing to measure is the first real
member run on an ECS deployment — the scratch-disk datadir (4′) and the rebuilt image's pin
route are the two paths this acceptance could not reach.

## The P1 contract (2026-09-17, fixed before the lanes start)

P1 is three lanes built the same way as P0 (one session each, a reviewer each, merge in the
order M1 → M2 → M3). What each lane may touch is named so that they do not collide.

### M1 — MySQL supply (`workspace/agent/install_mysql.go`, `workspace/Dockerfile`)

- **Image**: `libaio1t64`, `libnuma1`, `libncurses6` are installed by `apt` in `workspace/Dockerfile`
  (both architectures; ~250 KB). Until an image with them is deployed, `AF_DB_MYSQL_LIBS=<dir>`
  is prepended to `LD_LIBRARY_PATH` when `mysqld` / `mysql` are started — the test hook, and the
  documented workaround for older images (this container: `~/.local/share/af-dbtest/libs/usr/lib/x86_64-linux-gnu`).
- **`workspace-agent install-mysql [8.4]`** → `~/.local/share/agent-fleet/mysql/8.4/{bin,lib,share}`.
  Pins: `mysql` = `8.4.6`, `mysql_sha256` per build architecture (x86_64: the `minimal` tarball;
  arm64: the full tarball). URL: try `https://cdn.mysql.com/Downloads/MySQL-8.4/<file>` then
  `https://cdn.mysql.com/archives/mysql-8.4/<file>`; `<file>` = `mysql-<ver>-linux-glibc2.28-x86_64-minimal.tar.xz`
  or `mysql-<ver>-linux-glibc2.28-aarch64.tar.xz`. Download to a staging file, verify sha, then
  unpack — on arm64 **only** `bin/{mysqld,mysql,mysqladmin,mysqldump}`, `lib/private/*.so*`,
  `lib/plugin/*.so`, `share/`, then `strip` every ELF in place with the image's binutils (if
  `strip` is absent, keep them and say so on stderr). Staging is a fixed per-version dir wiped at
  start, under a per-version flock, like `install-postgres`. Exit 3 with the URL and both shas on
  mismatch; when the download is refused, the message names `cdn.mysql.com` and
  `AF_EGRESS_ALLOWLIST` (open question 4 stays open).
- After unpack, `ldd bin/mysqld` must show no `not found`; if it does, exit 3 naming the libraries
  and `AF_DB_MYSQL_LIBS`.
- `AF_DB_MYSQL_ROOT=<dir>` overrides the root, as `AF_DB_POSTGRES_ROOT` does.

### M2 — MySQL runtime and the Agent API (`workspace/agent/internal/afdb/`, `routes.go`)

- `Instance.Major` becomes a string (`"17"`, `"8.4"`); the registry file is new in P0 and not
  deployed, so no migration. Instance key `mysql-8.4`; state root as for Postgres; datadir
  `<state root>/mysql-8.4/data`; socket `~/.local/state/af-db/run/mysql-8.4/mysql.sock`; pid file
  `<state root>/mysql-8.4/mysqld.pid`; log `<state root>/mysql-8.4.log`; password
  `~/.config/agent-fleet/af-db/mysql-8.4.pass` (0600).
- **Init**: `mysqld --no-defaults --initialize-insecure --basedir=<root> --datadir=<datadir>`, then
  on the socket `ALTER USER 'root'@'localhost' IDENTIFIED BY '<pw>'` and
  `CREATE USER 'root'@'127.0.0.1' IDENTIFIED BY '<pw>'` + `GRANT ALL ON *.* … WITH GRANT OPTION`
  (with `--skip-name-resolve`, `127.0.0.1` does not match `localhost`).
- **Start flags**: `--no-defaults --basedir --datadir --socket --pid-file --log-error
  --bind-address=127.0.0.1 --port=<port> --skip-name-resolve --mysqlx=0
  --innodb-buffer-pool-size=64M --performance-schema=0 --innodb-flush-log-at-trx-commit=0`
  (the last one is the `fsync=off` analogue; dropped under `--persist`). Ready = `mysqladmin ping`
  on the socket.
- **Talking to MySQL**: shell out to `<root>/bin/mysql` and `mysqladmin` (both in the `minimal`
  tarball and in the arm64 subset); no new Go dependency. Idle count:
  `SELECT count(*) FROM information_schema.processlist WHERE id <> connection_id() AND user <> 'event_scheduler'`.
  Stop: `mysqladmin shutdown` → wait for the pid → only then the datadir (same rule as Postgres;
  the mysqld-spins-forever hazard is the reason).
- **Memory gate**: `af-db up mysql` refuses with exit 6 when `/sys/fs/cgroup/memory.max` is a
  number below 1 GiB, and says so (`AF_DB_MEM_GATE=0` disables the gate).
- **URLs**: socket `mysql://root:<pw>@localhost/<db>?socket=<sockpath>`; `--tcp`
  `mysql://root:<pw>@127.0.0.1:<port>/<db>`. `af-db url --format=go-dsn` prints the
  `go-sql-driver` form (`root:<pw>@unix(<sock>)/<db>` / `root:<pw>@tcp(127.0.0.1:<port>)/<db>`;
  for Postgres `go-dsn` is the URL). `af-db env` adds `AF_DB_URL_MYSQL`. Per-working-copy database
  naming, reconcile and `--db=NAME` exactly as for Postgres.
- **`status --json` gains** `version` (server version string) and `rssBytes` (VmRSS from
  `/proc/<pid>/status`; for Postgres the postmaster plus its children) per instance.
- **Test hooks**: `AF_DB_IDLE_SECONDS` overrides the 30-minute idle window (the loop test uses
  5 s); `AF_DB_MYSQL_ROOT`, `AF_DB_MYSQL_LIBS` as above.
- **Agent HTTP API** (the Console's source; registered in `workspace/agent/routes.go` next to
  `/env/toolchains`):
  - `GET /env/databases` → `{"engines":[{"engine":"postgres","major":"17","installed":true,
    "state":"absent|installing|starting|running|stopped|error","version":"17.11","rssBytes":n,
    "port":n,"datadir":"…","urlSocket":"…","urlTcp":"…","databases":{"<db>":"<dir>"},
    "lastUsedAt":"…","lastError":""}, {"engine":"mysql","major":"8.4",…}]}`. URLs carry the
    password; the CP relays and never persists this body.
  - `POST /env/databases/{engine}/start|stop|reset` (`stop?purge=1`). `start` returns at once
    with `state` `installing` or `starting` and runs the work in the Agent; `GET` reports
    progress and `lastError`. `stop` and `reset` are synchronous.
- **Acceptance (M2)**: from a HOME that has never seen `af-db`, with `AF_DB_MYSQL_ROOT` pointing
  at an unpacked `minimal` tarball and `AF_DB_MYSQL_LIBS` at the scavenged libraries:
  `af-db up mysql` → `af-db url mysql` → a `CREATE TABLE … JSON` / `SELECT j->>'$.a'` round trip
  through `mysql` → `status --json` shows `rssBytes` near 226 MB → with `AF_DB_IDLE_SECONDS=5`
  the loop stops it → `down --purge` leaves no `mysqld` and no datadir. Postgres's 4 PASS / 0 SKIP
  still green.

### M3 — Console card, CP proxy, documentation (`control-plane/routes.go`, `console/`, docs)

- **CP**: `GET /api/env/databases` and `POST /api/env/databases/{engine}/{action}` registered as
  `rest` next to `/api/env/toolchains` (`routes.go:769`). Nothing else in the CP.
- **Console**: a "Databases" card in the workspace settings Env tab
  (`console/src/features/settings/workspace/`, a new `EnvTabDatabases.tsx` beside `EnvTab.tsx`):
  one row per engine — version, state, resident size, port, the URL with a copy button and a
  socket / TCP toggle, Start / Stop / Reset (Stop offers "also remove the data"), an
  "installing…" state polled every 5 s while `installing|starting`, `lastError` shown inline.
  Strings through `console/src/lib/i18n` in en and ja; `oxlint` clean; dom tests modelled on
  `EnvTabNode.dom.test.tsx` with the API mocked; `NODE_OPTIONS=--max-old-space-size=3072 npm run build`
  passes.
- **Docs** (en/ja): the member guide gains the card and MySQL (memory: ~226 MB resident, stop it
  before a JVM build; arm64 install downloads 909 MB); `workspace/notes/environment.md` gains the
  one MySQL sentence; `guide/ref/features.md` a row if that table lists Env-tab features.
- **Acceptance (M3)**: dom tests and build green; a headless-Chromium screenshot of the card
  against a mocked `GET` is attached to the PR; the live card is verified after the next
  development deployment (it needs an Agent with M2 merged).

## P1 accepted (2026-09-17)

Same shape as P0: three lanes, a reviewer each, findings sent to the author by peer message,
merged M1 → M3 → M2 (M3 before M2 is harmless: the card only shows `lastError` while the Agent
has no `/env/databases`).

| Lane | Branch | Findings sent / fixed | Merged as |
|---|---|---|---|
| M1 supply (`install-mysql`, the three libraries and the `mysql` pins in `Dockerfile`) | `temp/svxno7x` | 7 / 7 | `53344c2e` |
| M3 Console (`EnvTabDatabases.tsx`, CP `rest` ×2, guide, environment note) | `temp/ski4oxb` | 14 / 13 | `3565b8c5` |
| M2 runtime (MySQL engine in `internal/afdb`, `Major` as string, `status` `version`/`rssBytes`, `GET|POST /env/databases`) | `temp/se2o2ag` | 12 / 12 | `3474fea0` |

**Acceptance, run by the parent from a HOME that had never seen `af-db`** (this container,
x86_64, the merged branch; `AF_DB_MYSQL_ROOT` at an unpacked `minimal` tarball and
`AF_DB_MYSQL_LIBS` at the scavenged libraries, because this image predates both the `mysql`
pin and the three libraries): `af-db up mysql` 15.9 s including `--initialize-insecure`; both
URL forms and `--format=go-dsn` as contracted; `CREATE TABLE … JSON` / `SELECT j->>'$.a'`
answered `42` over the socket **and** over `127.0.0.1` (so `root@127.0.0.1` exists);
`status --json` reported `version` 8.4.6 and `rssBytes` 233 MB; the password appeared nowhere
in the log or the registry; `down mysql --purge` left no `mysqld` and no datadir. Then, in the
same HOME, `af-db up` installed Postgres 17.11 from Maven Central and started it in 9.6 s, and
`TestPostgres|TestSchemaDialectParity` gave **4 PASS, 0 SKIP**; `down --purge` clean. The idle
stop with `AF_DB_IDLE_SECONDS` was observed by the M2 reviewer (2-second window, stopped on
the second tick), not re-run here — it needs the Agent's loop. `gofmt`, `go vet`,
`go test -count=1 ./...` on `workspace/agent` and `control-plane` are clean; one browser
capture test (`internal/browserx`, untouched by this ADR) failed once under the concurrent
acceptance load and passed twice alone.

### Contract corrections found by the reviews (the P1 contract above is superseded on these rows)

- **M1, arm64 subset**: add `lib/private/icudt*l/` (the ICU data directory; without it `mysqld`
  starts but warns `MY-013829` on every start), and exclude `lib/plugin/debug/` explicitly —
  GNU tar's `*` matches across `/`. Measured subset before strip: 241 files, 762 MB
  (`bin` 534 MB of which `mysqld` 514 MB; `lib/private` 136 MB; `lib/plugin` 82 MB; `share` 11 MB).
- **M1, x86_64 install time**: about 10 s for 66 MB → 446 MB (the guide's "a few minutes" is
  still the arm64 figure).
- **M1, today's images**: every deployed image predates the `mysql` pin and — unlike Postgres —
  there is no metadata fallback, so `install-mysql` exits 1 until the image is rebuilt; the
  Console card shows that as `lastError`.
- **M2, liveness**: when the pid file is missing, the registry pid is consulted; if `stop`
  fails and the process is alive, `--purge` is refused. The first version deleted the datadir
  under a running `mysqld` (measured: 1.32 M error lines in 38 s, `SIGKILL` only) — and
  `isPGRunning` from P0 had the same shape and was fixed with it. `mysqld` is `Wait()`ed so it
  never lingers as a zombie that defeats the pid check.
- **M2, init**: `ALTER USER` gets the password on stdin, not argv (`/proc/*/cmdline` is
  shared); an inherited `MYSQL_PWD` is dropped; the `.pass` file is written only after the
  `ALTER` succeeds; `CREATE DATABASE IF NOT EXISTS` (two concurrent `url` from a purged state
  raced to `ERROR 1007`).
- **M2, idle**: `AF_DB_IDLE_SECONDS` shortens the loop period as well as the window. The
  window itself is P0's two-stage one (threshold since `lastUsedAt`, then threshold since
  first seen idle), so the effective idle time is 30–60 minutes plus a period, not 30.
- **M2, `version`**: short form for both engines (`SHOW server_version` → `17.11`; `8.4.6`).
- **M2, HTTP**: `start` goes `installing` → `starting` → `running`; a failed `start` leaves
  `state=error` with `lastError`, which a later successful `stop` or `reset` clears; the
  state transition is a compare-and-swap so two `start`s do not race; every field is always
  present (no `omitempty`).
- **M3**: the URL is shown with the password masked and copied whole; `stopped` rows show no
  URL and only Start; `reset` is offered only when running; the "also remove the data" stop
  asks first; polling re-arms while the state stays `installing|starting` and stops on
  unmount. The tab that holds the card is "Toolchains", not "Env"; the guide says so.

Left as found: the `status --json` `databases` map is `null` after a purge (cosmetic);
`TestMemoryGateParsing` duplicates the parsing logic instead of calling it; the datadir
removal after a failed `ALTER` does not wait for the `SIGKILL`ed pid. Other sessions' test
residue was running in this container during the acceptance (a `mysqld` from
`TestMySQLIdleStop` and three `postgres` under `/tmp/tmp.*`); it was not touched.

**Next**: rebuild and deploy the development image (the `mysql` pin, the three libraries, the
`af-db` shim and the `postgres` pin route are all unexercised until then), then the live card
and the first ECS run with a scratch-disk datadir.

## On the rebuilt image (2026-09-17, first live run)

The development image was rebuilt from `9d5effa5` (the merge of #719) and deployed to this
container. It carries what P1 asked for: `/usr/local/bin/af-db`, the `postgres` / `mysql` pins
with their per-architecture shas in `versions.json`, and `libaio1t64` / `libnuma1` /
`libncurses6`. Both paths the P0 and P1 acceptances could not reach are now measured, from
HOMEs that had never seen `af-db`, and both found something.

**The pinned supply route works.** `install-postgres` took 1.9 s and `af-db up` 3.3 s; the
`control-plane` criterion gave **4 PASS, 0 SKIP** again; `install-mysql` downloaded the pinned
8.4.6 and the `mysql_sha256` from `versions.json` verified the real tarball, so the pin route —
untested until now, and without the Maven-metadata fallback Postgres has — is exercised.

- **The three libraries are not enough: `libaio.so.1` is not what trixie ships.** MySQL's
  binaries ask for `libaio.so.1`; `libaio1t64` installs `libaio.so.1t64` and **no compatibility
  symlink**, so on the rebuilt image `install-mysql` still exited 3 with
  `unresolved libraries: libaio.so.1`. The P1 acceptance never saw this because its
  `AF_DB_MYSQL_LIBS` directory contained a hand-made `libaio.so.1 -> libaio.so.1t64.0.2` link,
  scavenged before the bake existed. On 64-bit architectures the t64 library is ABI-identical,
  so the fix is the symlink — made by `install-mysql` itself, into `lib/private`, which is on
  the binaries' `RUNPATH` (`$ORIGIN/../lib/private`), so nothing touches `LD_LIBRARY_PATH` at
  run time and arm64 is covered by the same code. `ldconfig -p` (readable as `dev`) supplies
  the target; a soname with no `t64` counterpart is still reported by `mysqlCheckLDD`.
  Measured after the fix, no `AF_DB_MYSQL_LIBS` anywhere: install 7.5 s, `af-db up mysql`
  15.0 s, `SELECT j->>'$.a'` → `42` over the socket, `version` 8.4.6, `rssBytes` 233 MB,
  `down --purge` clean. `AF_DB_MYSQL_LIBS` stays as the escape hatch, not the procedure.
- **The Console card could not be verified, for a reason that has nothing to do with this
  ADR.** `GET /env/databases` on the running Agent answers **404** while `/env/toolchains`
  answers 200: `/proc/7/exe` points at `/home/dev/.local/bin/workspace-agent`, a hand-built
  P0-era binary (2026-09-17 06:29) left behind by a lane during the P0 acceptance. The
  entrypoint's `exec workspace-agent` goes through `PATH`, where `~/.local/bin` wins, and
  `~/.local` survives both restart and recreate — so one stray build silently becomes the
  container's Agent forever. The `af-db` shim (`exec workspace-agent af-db "$@"`) has the same
  shape, while the Go code already hardcodes `/usr/local/bin/workspace-agent` for its own
  re-exec (`paths.go:126`, `afdb/cmd.go:770`, `afdb/mysql.go:77`). The stray binary was removed
  and the workspace restarted; `CMD` and the `af-db` shim now name
  `/usr/local/bin/workspace-agent` so it cannot happen again from an image rebuild onwards. The
  session commands that still call a bare `workspace-agent` (`record-exit`, `record-terminal`,
  `install-kiro --if-needed`, `install-awscli`) have the same shape and were left alone: they
  run in the member's own shell, where the shadow is at least visible. Until the rebuilt image
  is deployed, the card's healthy states stay unseen — only `lastError` is reachable.

### After the restart: the card's API is live, and two more corrections

With the stray binary gone and the workspace restarted, `/proc/7/exe` is the image's agent and
`GET /env/databases` answers 200 with every field present. Driving it: `POST …/postgres/start`
went `starting` → `running` in under 6 s (`version` 17.11, `rssBytes` 47–49 MB, the allocated
port), and `af-db url` from this working copy added its database to the payload.
`POST …/mysql/start` ended in `state=error` — this image predates the `libaio` fix above, which
is exactly what a member on today's image would see.

- **A registry from an older agent stops everything.** The first `start` after the restart
  failed with `registry parse: json: cannot unmarshal number into Go struct field
  Instance.instances.major`: the P0 build wrote `"major": 17` as a number, P1 made it a string,
  and `~/.config` survives recreate. The contract's "the registry is new in P0 and not deployed,
  so no migration" holds for images, not for a HOME where an older build ever ran — and an
  unreadable registry disables the very verbs that could repair it. `Instance.UnmarshalJSON` now
  takes both forms, and the parse error names the file. (`af-db status` was also hiding the
  problem behind an empty instance list while the HTTP path reported it.)
- **`lastError` has to carry the reason.** The card showed `install-mysql 8.4 failed: exit
  status 3`; the `libaio.so.1` line that explains it went to the Agent's log only. The installer's
  last lines are now appended to the error, the same lesson the Console already learned about
  generic `*_failed` codes.
- **Noted, not changed**: `urlSocket` in the API payload carries the password in clear, because
  decision 9's card masks it for display and copies it whole — so CP relays a workspace
  credential to the browser, where `af-db status --json` deliberately carries none.

### The card's URL was for a database nobody had (contract change, M2 + M3)

The first look at the rendered card found the copy button handing out
`postgres://…/af_dev_12fbd7` — `DBNameFor("/home/dev")`, the **Agent process's own directory**.
`psql` with that URL answers `FATAL: database "af_dev_12fbd7" does not exist`, while the
registry's only database was `af_agent_fleet_wip_szkxzgu_9af42b` for this working copy. The
Agent cannot resolve "the caller's working copy" — it has only its own — so an engine-level URL
is wrong by construction, and the HTTP path never creates what it advertises.

`EngineStatus` therefore drops `urlSocket` / `urlTcp` and `databases` becomes a list:
`[{name, dir, urlSocket, urlTcp}]`, sorted by name, filled only while the engine is running.
The card renders one block per database — name, working copy, the Socket/TCP toggle and Copy —
and says "run `af-db url` in a working copy" when the list is empty. The empty list is also a
`[]`, which retires the `databases: null` cosmetic from the P1 acceptance. The guide's card
section says the same in both languages. Verified by unit tests on both sides
(`TestDatabaseEntriesPerWorkingCopy`, and a DOM test that copies the second row's own URL);
the live card gets this at the next image bake.

### Confirmed on the image that carries the fixes (2026-09-17, same day)

The development image was rebuilt from the merge of #721 and deployed here. Everything the two
runs above could only promise is now measured on it, with no workaround anywhere:

- `/proc/7/exe` is `/usr/local/bin/workspace-agent` and the shim reads
  `exec /usr/local/bin/workspace-agent af-db "$@"` — the PATH shadow can no longer take the Agent.
- `af-db up mysql` from a HOME that had never seen it: **27.6 s** end to end, with
  `[install-mysql] linked libaio.so.1 -> /lib/x86_64-linux-gnu/libaio.so.1t64 (Debian t64 soname)`
  in the log and `AF_DB_MYSQL_LIBS` unset. `down mysql --purge` clean.
- `install-pg-client` into a fresh HOME: **1.8 s** (17.11-0+deb13u1) — the figure the earlier run
  could not take because the client was already there.
- `GET /env/databases` carries the new shape (no engine-level URL; `databases` is a list), and
  **the URL the card copies connects**: both the socket and the TCP form of
  `af_agent_fleet_wip_szkxzgu_9af42b` answer `select current_database()` with their own name,
  where the previous image's URL answered `database "af_dev_12fbd7" does not exist`.

What is still untested stays untested: the scratch-disk datadir on ECS, and arm64 MySQL.

**Next**, unchanged except for what this run closed: the Console card on a workspace whose
Agent is the image's, the first ECS run with a scratch-disk datadir, and arm64 MySQL.
