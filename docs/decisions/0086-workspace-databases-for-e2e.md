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
