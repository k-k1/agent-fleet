---
audience: "anyone looking up af-db's verbs, options, connection URLs and exit codes"
source_of_truth: "af-db's usage, from the workspace-agent in the workspace"
---

# af-db

English | [日本語](af-db.ja.md)

Runs PostgreSQL and MySQL inside the workspace and hands out connection URLs. It installs and starts the server on
first use and gives each working copy its own database. How to use it for tests and from the Console is in
[Run database-backed tests](../../user/workspace/databases.md).

## Synopsis

```sh
af-db up       [engine] [--major N] [--durable] [--ephemeral]
af-db url      [engine] [--major N] [--db=NAME] [--tcp] [--format=go-dsn]
af-db env      [engine] [--major N] [--db=NAME] [--tcp]
af-db connect  [engine] [--major N] [--db=NAME]
af-db create   [engine] [--major N] --db=NAME
af-db drop     [engine] [--major N] --db=NAME
af-db reset    [engine] [--major N] [--db=NAME]
af-db down     [engine] [--major N] [--purge]
af-db status   [engine] [--major N] [--json]
```

Running it with no arguments, or with a verb it does not know (`--help` included), prints the usage and exits with
status 2.

| Argument | Values |
|---|---|
| `engine` | `postgres` (the default) or `mysql` |
| `--major N` (`--major=N`) | PostgreSQL `16`, `17` (the default) or `18`. MySQL `8.4` (the default and the only one) |

Each engine and major is its own server with its own data. `af-db up --major 16` and `af-db up` run two PostgreSQL
servers side by side.

## Verbs

| Verb | What it does | Starts the server |
|---|---|---|
| `up` | Installs the engine if needed and starts it. Prints `<engine>-<major> running on port <port>` | Yes |
| `url` | Creates the database if needed and prints its connection URL | Yes |
| `env` | Like `url`, but prints `export` lines (below) | Yes |
| `connect` | Creates the database if needed and replaces itself with `psql` / `mysql` connected to it | Yes |
| `create` | Creates a database named with `--db`. An existing one is left as it is and counts as success | No: exits with status 5 when stopped |
| `drop` | Deletes a database named with `--db`, with everything in it. On PostgreSQL, open connections to it are cut | No: exits with status 5 when stopped |
| `reset` | Drops and recreates a database, leaving it empty | No: exits with status 5 when stopped |
| `down` | Stops the server. `--purge` also removes its data directory | — |
| `status` | Shows the state, port, socket and databases. Without `engine`, every server af-db knows | — |

## Options

| Option | Verbs | What it does |
|---|---|---|
| `--db=NAME` | `url`, `env`, `connect`, `create`, `drop`, `reset` | Uses the database `NAME` instead of the working copy's own. Required for `create` and `drop` |
| `--tcp` | `url`, `env` | Prints a URL that connects over TCP on `127.0.0.1` instead of the unix socket |
| `--format=go-dsn` | `url` | Prints the `go-sql-driver/mysql` form. For PostgreSQL the output is the same URL |
| `--durable` | `up` | Flushes every commit to disk (`fsync` on PostgreSQL, a flush at each commit on MySQL) |
| `--ephemeral` | `up` | Puts the data directory on the working disk, which is faster and **wiped when the workspace stops**. Without a working disk it is ignored with a notice and the data stays in the home |
| `--purge` | `down` | Removes the data directory: **every database on that engine and major** |
| `--json` | `status` | Prints JSON instead of text |

- `--durable` and `--ephemeral` take effect only when the server starts. On a running server they are ignored without
  a message: run `af-db down` first.
- They stick to the server: a later `af-db up` without them keeps them, and no af-db option turns them off again.
- `--ephemeral` does not move existing data. It starts the server on a new, empty data directory on the working disk;
  the data in the home stays where it was and is no longer used.
- `--persist` is accepted as `--durable`, with a notice.

## Which database

Without `--db`, af-db uses the working copy's database, named `af_<directory>_<6 hex digits>`, for example
`af_webshop_3f9a1c`. The working copy is, in order:

1. the working copy of the session the command runs in,
2. the top of the git repository in the current directory,
3. the current directory.

Two sessions on the same working copy share its database.

When a working copy's directory no longer exists, af-db tries to drop its database the next time `up`, `url`, `env`,
`connect` or `status` (but not `status --json` without an engine) finds the server running. A drop that fails (on PostgreSQL, while something is connected
to it) is printed as a warning and tried again next time. A database created with `create --db` or `url --db` belongs
to no working copy and is never dropped this way. `connect --db=<name>` with the working copy's own name counts as the
working copy's database.

A name given with `--db` matches `^[a-z_][a-z0-9_]{0,62}$`: lower-case letters, digits and `_`, not starting with a
digit, at most 63 characters. Any other name exits with status 2.

## Connection URLs

The default URL connects over the unix socket. PostgreSQL's user is `postgres` and MySQL's is `root`; the password is
generated per server and kept in the workspace.

| Engine | Form | Example |
|---|---|---|
| PostgreSQL | socket (default) | `postgres://postgres:<password>@/<db>?host=<socket dir>&port=5432&sslmode=disable` |
| PostgreSQL | `--tcp` | `postgres://postgres:<password>@127.0.0.1:5432/<db>?sslmode=disable` |
| MySQL | socket (default) | `mysql://root:<password>@localhost/<db>?socket=<socket dir>/mysql.sock` |
| MySQL | `--tcp` | `mysql://root:<password>@127.0.0.1:3306/<db>` |
| MySQL | `--format=go-dsn` | `root:<password>@unix(<socket dir>/mysql.sock)/<db>` |
| MySQL | `--format=go-dsn --tcp` | `root:<password>@tcp(127.0.0.1:3306)/<db>` |

`env` prints two lines for `eval`:

```sh
$ af-db env
export AF_DB_URL_POSTGRES="postgres://postgres:…@/af_webshop_3f9a1c?host=…&port=5432&sslmode=disable"
export DATABASE_URL="postgres://postgres:…@/af_webshop_3f9a1c?host=…&port=5432&sslmode=disable"
```

With `mysql` the first line is `AF_DB_URL_MYSQL`.

### Ports

The server listens on `127.0.0.1` only. af-db first tries the engine's usual port, 5432 for PostgreSQL and 3306 for
MySQL, then the ports above it up to base + 63, and takes a free port anywhere when all of those are in use. A server keeps
the port it got across restarts while that port is free. Read the port from `af-db status` or the URL rather than
assuming one.

### Variables in a new session

A session started while a server is running gets the client variables, so `psql` and `mysql` connect with no
arguments:

| Engine | Variables |
|---|---|
| PostgreSQL | `PGHOST`, `PGPORT`, `PGUSER`, `PGPASSWORD`, and `PGDATABASE` and `AF_DB_URL_POSTGRES` once the working copy has a database |
| MySQL | `MYSQL_UNIX_PORT`, `MYSQL_TCP_PORT`, `MYSQL_PWD`. There is no variable for the database: run `mysql <db>` |

A shell opened before the server started has none of them. `af-db connect` works from any shell.

## status

```
$ af-db status
mysql-8.4: stopped
postgres-17: running  port=5432  sock=/home/dev/.local/state/af-db/run/postgres-17  pid=4121
  db: af_webshop_3f9a1c  dir=/home/dev/repos/webshop
  db: scratch (shared)
```

`(shared)` marks a database named with `--db`. With nothing set up yet it prints `af-db: no instances configured`.

`--json` with an engine prints `{"state":"not-configured"}` alone for an engine never started. Otherwise it prints
`engine`, `major`, `state` (`running` or `stopped`), `pid`, `port`,
`sockdir`, `datadir`, `databases` (name → working copy, empty for a named one) and `lastUsedAt`, plus `version` and
`rssBytes` while running. Without an engine it prints `instances`, one row per server with `engine`, `major`, `state`,
`port` and `datadir`, plus `version` and `rssBytes` while running. This form does not drop the databases of removed
working copies.

## Where things live

| What | Where |
|---|---|
| Data | `~/.local/state/af-db/` (survives a stop and a recreate). With `--ephemeral`, `af-db/` on the working disk |
| Sockets | `~/.local/state/af-db/run/<engine>-<major>/` |
| Server binaries | `~/.local/share/agent-fleet/<engine>/<major>/` |
| Clients | `psql`, or `mysql` and `mysqldump`, in `~/.local/bin`, put there when the server starts |

The first start downloads the server, so it can take a few minutes. A server with no connection for 30 minutes is
stopped by the workspace. Turning on **Start with the workspace** on **⚙ Settings → Databases** starts it with the workspace
instead.

## Environment

| Variable | Effect |
|---|---|
| `AF_DB_MEM_GATE=0` | Skips the memory check before MySQL starts (below) |

## Exit status

| Status | Meaning |
|---|---|
| 0 | Done |
| 1 | Another error, such as SQL the server refused (a `reset` on PostgreSQL while something is connected to the database) |
| 2 | Usage error: an unknown verb or option, a missing `--db`, an invalid name |
| 3 | The install failed, including a `--major` that is not offered |
| 4 | The server failed to start |
| 5 | The server is not running (`create`, `drop`, `reset`). Run `af-db up` first |
| 6 | Not enough memory: MySQL needs a workspace memory limit of at least 1 GiB |

Errors go to standard error as `af-db: <message>`.

## Related

- [Run database-backed tests](../../user/workspace/databases.md)
- [af-scratch](af-scratch.md): the working disk `--ephemeral` uses
