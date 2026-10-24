---
audience: "anyone setting up or resetting the workspace's environment"
updated: "2026-10"
---

# 17. Workspace environment — settings, recreating, test databases, a local AWS emulator

English | [日本語](17-workspace-environment.ja.md)

This chapter covers the environment your workspace runs in: environment settings, recreating the workspace or cleaning home, databases for tests, and trying AWS code against a local emulator.

## Environment settings and recreating the workspace

In **⚙ Settings → the "Toolchains" tab** you can adjust the workspace environment. Changes **apply to sessions /
shells started afterwards** (running ones and existing processes pick them up after you stop and then start the
workspace again).

- **Time zone (TZ)**: the default is Japan time. Applying a change requires stopping and starting the workspace.
- **Node.js / Java (JAVA_HOME) / Go**: pick the versions to use. The Node.js and Java lists also offer
  versions that are **not in this workspace yet**; picking one shows an **Install** button that fetches it
  right there (Java about 200MB; both go into your home volume, so they survive restarts). When a newer
  patch of the Java version in use is out, **Update to …** fetches it the same way. Sessions started after
  it finishes use it (Java as `JAVA_HOME`), with no stop and start needed.
- **Agent CLI updates**: "Update the agent CLIs and rtk to the latest on start" (covers claude / opencode / codex / cursor / GitHub Copilot / [Antigravity (agy)](09-agents.md#antigravity-agy) / rtk). Default is OFF (pinned to the versions baked into the image). Kiro is not part of this toggle; its version is fixed by the image rebuild / on-demand install and its own auto-update is kept off. Neither is Muse Code: it is installed from its card at the build this image pins and never updates itself; when a newer pinned build arrives with the image, the card offers **"Update Muse Code"** ([09](09-agents.md#muse-code)). lcpp has no CLI in the workspace to update: its engine belongs to the deployment.

### Recreating the workspace (danger zone)

In **⚙ Settings → the "Danger zone" tab** is **"Recreate the workspace"**. It discards the
container and rebuilds it from the latest image; pressing **"Recreate"** shows a confirmation.

Not every deployment offers this tab. Recreating and cleaning home remove files from the home
itself, and on some deployments the home is out of the control plane's reach
([ref/deploy-targets](../ref/deploy-targets.md)); there the tab is not shown. Stopping and starting
the workspace from the workspace bar works everywhere.

- **What is lost**: running sessions, and **cloned repositories (`~/repos`, including uncommitted changes)**.
  `~/repos` is the **only** thing deleted.
- **What stays**: everything else in your home (`~`) remains. Logins and connections (GitHub / Bitbucket /
  Claude etc.), `~/.local` (claude / node etc.), and your settings and caches are preserved, because the home
  volume is reattached to the recreated container.

In short: "**only `~/repos` is deleted, and the container is rebuilt from the latest image. The rest of home
(logins, connections, `~/.local`, etc.) stays**". Use it when you want to pick up an image update or the
environment is broken. **Uncommitted changes are lost**, so push / commit before running it ([06](06-code.md)).

### Cleaning home (an even deeper reset)

Since recreating deletes only `~/repos`, it won't fix problems on the home side (a broken claude install in
`~/.local`, corrupted caches or config files, and so on). In that case use **"Clean home"**, in the same
"Danger zone" tab. It deletes **your entire home except logins and connections** (`~/repos`, `~/.local`, caches,
settings) and rebuilds from the latest image, a deeper reset.

- **What stays**: logins and connections (GitHub / Bitbucket / Claude) **only**.
- **What is lost**: running sessions, cloned repositories, and **everything else in home**, including
  `~/.local`, caches, and settings.

Try "Recreate" first to see if it fixes things, and use "Clean" only when that doesn't.
Both operations **lose uncommitted changes**.

## Running database-backed tests

Postgres is available inside the workspace without Docker or any external service. `af-db url`
returns a connection URL for the current working copy's database, downloading, initialising, and
starting the server on first call if needed (first use may take a few minutes). The server idle-stops
after 30 minutes of no connections.

```bash
# Export DATABASE_URL into the current shell
eval "$(af-db env)"
go test -count=1 ./...

# Or pass the URL under your project's own variable name
MY_DB_URL="$(af-db url)" go test -count=1 ./...
```

Check what is running with `af-db status`. When you are done, stop the server:

```bash
af-db down
```

Stop it before a memory-intensive build: the server holds ≈ 47 MB of the workspace's memory
quota, and a heavy JVM build next to it can exhaust it.

A few things worth knowing:

- **One database per working copy.** Two sessions that share the same working copy share the same
  database. To address a named database explicitly, use `af-db url --db=<name>`.
- **`af-db url --tcp`**: use when the test driver requires TCP (for example,
  `jdbc:postgresql://127.0.0.1:…`). The default URL uses a unix socket, which most Go and Python
  clients support but JDBC cannot.
- **Your data survives a Stop → Start.** The database files live in your home
  (`~/.local/state/af-db`), which is workspace state like anything else there. Only two things
  delete them: `af-db down --purge`, and deleting a database from the Databases tab.
- **`af-db up --durable`**: also flushes every commit to disk (`fsync`). The default trades that
  for speed, which is the right trade for test data: a crash of the workspace can lose the last
  few transactions, but a normal stop never does. (`--persist` is the old name for this flag and
  still works.)
- **`af-db up --ephemeral`**: the opposite: put the files on the task-local disk, which is
  faster and is **wiped when the workspace stops**. Use it when you know the contents are a
  fixture. Where the deployment has no task-local disk, it says so and uses your home.
- **`psql` and `mysql` are on PATH, and connect with no arguments.** Starting an engine
  installs its client and puts it there (`psql` is a separate download; `mysql` ships with the
  server). A session launched afterwards carries `PGHOST` / `PGPORT` / `PGUSER` / `PGPASSWORD` /
  `PGDATABASE` and `MYSQL_UNIX_PORT` / `MYSQL_PWD`, so `psql` opens this working copy's database
  and `mysql <name>` opens a MySQL one.
- **The port is stable**: 5432 for Postgres and 3306 for MySQL, the numbers your framework already
  defaults to. It only moves if something else in the container holds it when the server starts,
  and the server keeps whatever it got across later restarts, so a connection string in the
  application you are developing keeps working. `af-db status` always shows the live number.
- **`af-db connect [engine] [--db=NAME]`** opens the client on a database whatever your shell
  already has in it; it resolves the socket, password and user itself, and starts the engine
  first if it is not running. Use it in a shell you opened before the server was up.

### MySQL

MySQL 8.4 is also available. Start it the same way:

```bash
af-db up mysql
eval "$(af-db env)"
```

`eval "$(af-db env mysql)"` exports `AF_DB_URL_MYSQL` (and `DATABASE_URL`) into the shell that asked;
nothing sets it for you. Pass the connection in the form your driver expects with
`af-db url mysql --format=go-dsn` (for `go-sql-driver/mysql`).

Things to know about MySQL specifically:

- **Memory: ≈ 226 MB resident** (MySQL holds an InnoDB buffer pool even at idle). Stop it
  before a JVM build or another memory-intensive task (the workspace's cgroup is shared with
  all sessions): `af-db down mysql`.
- **arm64 install downloads 909 MB.** The full tarball is required on arm64 (there is no
  `minimal` build for that architecture). The installer keeps only what af-db needs and strips
  the debug sections, so **the installed tree is ≈ 147 MB**, smaller than x86_64's. Measured
  end to end: about 30 seconds.
- **x86_64 install downloads ≈ 63 MB** (the `minimal` tarball, ≈ 446 MB installed).
- The memory guard refuses `af-db up mysql` when the workspace's cgroup limit is below 1 GiB
  and says so; run `af-db down postgres` first if Postgres is running.

### Console Databases tab

The workspace settings **Databases** tab shows every engine's version, resident size
(MB), port, connection URL and state, and lets you start and stop an engine, and create, delete or reset a
database, without opening a terminal.

- **Start** on an uninstalled engine downloads and installs it first, then starts it. The card
  polls automatically while the state is `installing` or `starting`.
- **Stop** has a "Stop and remove data" option (`--purge`): use it to free the datadir space. It
  takes **every database on that engine** with it, other working copies' included, and any
  shared one made with `--db=`. To start one over, use **Reset** on its row instead.
- **Start with the workspace** keeps that engine running: tick it and the Agent starts the engine
  whenever the workspace starts, with no terminal and no button. It only appears for an engine
  that has been started at least once, so booting never downloads a server. Off by default:
  a database costs memory even when nothing is querying it.
- **Connect** opens a Shell session in your home and starts `psql` / `mysql` on that row's
  database (`af-db connect`). Quitting the client leaves you in the shell.
- **Create** makes a database with the name you type (same as `af-db create --db=<name>`). Use it
  for anything that is not one working copy's database: a scratch schema, a second database for
  one project, one shared between sessions. Names are lower-case letters, digits and `_`, not
  starting with a digit; the button stays disabled with the reason shown until the name is usable.
- **Delete** removes one database and everything in it (same as `af-db drop --db=<name>`), after a
  confirmation that names it. Anything still connected to that database is disconnected.
- **Reset** empties one database: it is dropped and recreated (same as `af-db reset`). A
  confirmation is shown before anything is deleted.
- **One row per database**, with the working copy it belongs to; Copy hands you the URL of
  *that* database. When there is none yet, create one here or run `af-db url` in a working copy.
- The **socket / TCP toggle** on the URL lets you copy whichever form your driver needs.
- Any error from the last operation is shown inline under the engine row.

The tab becomes active once the workspace is running; when the workspace is stopped it shows
a placeholder and makes no API calls.

## Trying AWS code against a local emulator (MiniStack)

To try code, a template or a script without touching a real account, you can run
[MiniStack](https://github.com/ministackorg/ministack), an open-source AWS API emulator, inside the workspace.
It needs no Docker when installed from PyPI, starts in seconds and stays small. It is not part of the image, so
install it yourself:

```sh
python3 -m venv ~/.local/ministack && ~/.local/ministack/bin/pip install ministack
GATEWAY_PORT=14566 ~/.local/ministack/bin/ministack
```

Ports are shared with your other sessions: pick a free one (the default 4566 may be taken) and stop the server
when you are done.

**Point a dedicated profile at it, and name that profile every time.** Pick a profile name that is used nowhere
yet (not in `aws configure list-profiles`, not in `af-aws-exec --list`, not in `~/.aws/credentials`), because a
`[ministack]` entry in `~/.aws/credentials` would win over the keys below and send real credentials to the
emulator, and a Settings profile of the same name stops being exported. Then add a new section outside the managed
block of `~/.aws/config`:

```ini
[profile ministack]
region = us-east-1
endpoint_url = http://127.0.0.1:14566
aws_access_key_id = 000000000001
aws_secret_access_key = test
```

and run `aws --profile ministack …`. The profile carries the endpoint only for clients that read `endpoint_url`
from a profile: the AWS CLI v2 and current SDKs do; AWS SDK for Java 1.x, JavaScript v2 and Go v1 do not, and
neither does a tool that sets its own endpoint. Those go to real AWS with the dummy keys unless you give the
client the endpoint itself (Terraform's `endpoints` block, the SDK's endpoint option). An exported
`AWS_ENDPOINT_URL*` or `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true` overrides the profile's endpoint too, so
check your shell for them.

Do **not** export dummy keys (`AWS_ACCESS_KEY_ID=test`) instead. Every command would then carry them, and one that
forgets `--endpoint-url` sends its request to real AWS (where the dummy keys are rejected) instead of stopping
with "Unable to locate credentials" before anything leaves the workspace. How much a forgotten profile protects
you depends on the workspace: where a command without a profile stops with that error (see `af-aws-exec` above),
it stops; where the workspace's own role is handed back (`AF_WS_WORKLOAD_AWS=1`) or on the native runtime with
the machine's own credentials, a command without a profile runs as that identity against real AWS. There,
never leave the profile out. MiniStack is never run through `af-aws-exec`, which removes endpoint settings on
purpose.

- **Sharing one server.** A 12-digit access key becomes the account id, and resources are kept apart per
  account, so sessions that must not see each other's resources use different keys. Separation by region
  depends on the service (S3 buckets, for one, are visible from every region), so do not rely on a region to
  keep two sessions apart.
- **What does not work here.** RDS, ElastiCache and ECS start real Docker containers, and a workspace has no
  Docker. RDS still answers `available` with an endpoint such as `localhost:5432` that no database of its own
  is behind; another Postgres in the workspace may be listening there. For a database, use `af-db`
  ([Running database-backed tests](17-workspace-environment.md#running-database-backed-tests)).
- **State** is in memory and lost when the server stops. `PERSIST_STATE=1` keeps service state but not S3
  object contents, which need `S3_PERSIST=1` as well; both default to directories under `/tmp`, so set
  `STATE_DIR` and `S3_DATA_DIR` to directories under your home.
- **It is an emulator.** A template or IAM policy that works against it may still fail on AWS; use it to
  iterate, and check the real thing through `af-aws-exec` before you rely on it.
