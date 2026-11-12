---
audience: "anyone looking up what af-scratch moves, where to, and its exit codes"
source_of_truth: "workspace/af-scratch.sh, installed as /usr/local/bin/af-scratch"
---

# af-scratch

English | [日本語](af-scratch.ja.md)

Moves build output onto the workspace's working disk and leaves a symlink in its place. Only some deployments give a
workspace a working disk: there the home is network storage, slow for trees of many small files, and the working disk
is local and fast. **Everything on the working disk is gone when the workspace stops**, so put only output a build can
regenerate there. Where the workspace has no working disk, af-scratch does nothing.

Separately from af-scratch, the workspace's start moves the Go build cache, the Go module cache and uv's cache onto
the working disk when it is 30 GiB or more (by default; a deployment can change the threshold and the list) and can be
written. By default nothing moves `node_modules` or npm's cache, and `.venv` moves only when you name it yourself.

## Synopsis

```sh
af-scratch <dir>...
af-scratch --status
af-scratch --auto [<root>]
af-scratch --repair
af-scratch --seed
```

| Form | What it does |
|---|---|
| `<dir>...` | Moves each directory to the working disk and replaces it with a symlink. A directory that does not exist yet becomes a symlink to its place there |
| `--status` | Lists what is moved now, with the size of each |
| `--auto [<root>]` | Moves the output the projects under `<root>` (default: the current directory) are about to produce. The workspace runs this on every new clone and working copy |
| `--repair` | Recreates the working-disk directory of each link on af-scratch's list that a stop left dangling, up to `AF_SCRATCH_REPAIR_MAX`. The workspace runs this at every start, before any session |
| `--seed` | Until it has once scanned without an error: finds links made by an older af-scratch, which kept no list, adds them to it and recreates theirs too. The workspace runs this at start, for up to `AF_SCRATCH_SEED_TIMEOUT` seconds before any session, then in the background |

With no argument it prints the usage and exits with status 1. There is no `--help`: af-scratch would treat it as a
directory name.

## Where things go

A directory goes to `$AF_WS_SCRATCH/artifacts/` followed by its own absolute path, so two projects' `target` never
collide:

```
$ af-scratch target
af-scratch: target を作業ディスクへ移しています…
af-scratch: target -> /scratch/artifacts/home/dev/repos/shop/target（Workspace 停止で消えます）
```

After a stop the symlink stays, but what it points to is gone, and builds do not create it through the link: Gradle
fails with `Failed to create parent directory`, and Maven and cargo refuse a dangling link the same way. So af-scratch
keeps a list of the links it made, in `~/.local/state/af-scratch/links`, and at every start, before any session runs,
the workspace runs `af-scratch --repair` to recreate each one's directory, empty. Running `af-scratch target` or
`--auto` on a dangling link recreates it too. Entries whose link is gone are dropped from the list at the next start.

A directory is recreated only when the link points under `$AF_WS_SCRATCH/artifacts/` and no part of the path below
that is a symlink, `..` or a file. A link that points anywhere else is yours, and af-scratch creates nothing through
it; a refused path is named in the start log.

A link stays dangling after a start, and needs `af-scratch <dir>` (or `af-scratch --repair`) by hand, when:

- its path contains a newline: af-scratch warns and does not put it on the list;
- the list has more dangling links than `AF_SCRATCH_REPAIR_MAX` (default 500): the rest stay on the list, the start
  log says how many, and the next `--repair` continues;
- it was made before the list existed, and `--seed` has not reached it yet or cannot find it (see below).

## Moving a directory yourself

| The directory | What happens |
|---|---|
| Is already a symlink | Nothing, with a note naming its target. If it points under `$AF_WS_SCRATCH/artifacts/` and dangles, its directory is recreated, empty |
| Exists | It is moved to the working disk, once (this run is slow: everything is read off the home), then linked. **Whatever was already at its place on the working disk is deleted first** |
| Does not exist | Its place on the working disk is linked, made empty if it is not there. If it is there (an earlier move, before a stop), it is reused with what it holds |
| Is named `node_modules` | Refused. npm replaces a symlinked `node_modules` with a real directory on the next install, and `npm ci` empties the working-disk side first |
| Is named `.venv` | Moved, with a warning: `uv venv` and `uv sync` work through the link, `python3 -m venv .venv` fails on it |

af-scratch does not check whether git tracks what you name. **Never name tracked files or uncommitted work**: a stop
deletes them.

## What `--auto` moves

It looks for project files up to three levels under `<root>`, without descending into `.git`, `node_modules`, `.venv`,
`target`, `build` or `dist`:

| Project file | Directory it moves |
|---|---|
| `Cargo.toml`, `pom.xml` | `target` beside it |
| `build.gradle`, `build.gradle.kts` | `build` beside it |

For each of these directories it follows stricter rules than the manual form:

- a symlink is left alone (it may be a link you made, to share with another working copy), except that a dangling
  link under `$AF_WS_SCRATCH/artifacts/` gets its directory back;
- an existing directory git does **not** ignore is left alone;
- an existing directory git ignores is moved and linked;
- a missing one is linked to a new empty directory, so the first build already writes to the working disk.

A fresh checkout therefore has an empty `target` or `build` link. A check such as `[ -d build ] || …` takes it for a
finished build, so run builds unconditionally.

`node_modules` and `.venv` are never moved by `--auto`. Where the workspace has no working disk, `--auto`, `--repair`
and `--seed` exit with status 0 and do nothing. They always exit with status 0.

## `--repair` and `--seed`

`--repair` reads only the list, so a start pays a check per link and never walks `~/repos`. Writes to the list take a
lock, so an `--auto` running at the same time is neither lost nor recorded twice.

`--seed` covers links made before the list existed. It looks for `target` and `build` symlinks up to
`AF_WS_SCRATCH_AUTO_DEPTH` + 1 levels under `~/repos`, the depth `--auto` uses for a clone there. It adds the ones
pointing under `$AF_WS_SCRATCH/artifacts/` to the list and recreates their directories. When the whole scan succeeded it
leaves `~/.local/state/af-scratch/seeded` and does nothing on later starts. When a directory could not be read or a link
not recorded, it leaves no marker, and the next start scans again. At start the workspace waits for it up to
`AF_SCRATCH_SEED_TIMEOUT` seconds (default 20), then lets it finish in the background, so on that one start a link it
has not reached yet can still dangle for a while. A link made by hand deeper than that, before the list existed, is not
found: run `af-scratch <dir>` on it once.

## `--status`

```
$ af-scratch --status
/home/dev/repos/shop/target                                  412M
/home/dev/repos/app/build                                    96M
```

It lists directories whose original path is still a symlink, for original paths up to six levels deep counted from `/`
(`/home/dev/repos/shop/app/target` is six). When nothing was ever moved it prints `逃がしているものはありません。`;
when things were moved but none is linked any more, it prints nothing.

## Environment

| Variable | Effect |
|---|---|
| `AF_WS_SCRATCH` | The working disk. Set by the deployment; when it is unset, af-scratch does nothing |
| `AF_WS_SCRATCH_AUTO=0` | Turns `--auto` off |
| `AF_WS_SCRATCH_AUTO_DEPTH` | How many levels `--auto` looks under `<root>`. Default 3. `--seed` looks one level deeper, under `~/repos` |
| `AF_SCRATCH_REPAIR_MAX` | How many dangling links one `--repair` recreates. Default 500 |
| `AF_SCRATCH_SEED_TIMEOUT` | How many seconds the start waits for `--seed` before leaving it to the background. Default 20 |

## Exit status

| Status | Meaning |
|---|---|
| 0 | Done. `--auto`, `--repair` and `--seed` always end here |
| 1 | No working disk (`AF_WS_SCRATCH` is unset), no argument, or a `node_modules` was named (the other directories named are still handled). Also a move or link that failed, with the shell's error, and nothing after it is handled |

## Related

- [Recreate the workspace](../../user/workspace/recreate.md): what survives a stop, a recreate and a clean home
- [af-db](af-db.md): `af-db up --ephemeral` puts a database on the working disk
