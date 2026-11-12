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

## Synopsis

```sh
af-scratch <dir>...
af-scratch --status
af-scratch --auto [<root>]
```

| Form | What it does |
|---|---|
| `<dir>...` | Moves each directory to the working disk and replaces it with a symlink. A directory that does not exist yet becomes a symlink to a new empty one |
| `--status` | Lists what is moved now, with the size of each |
| `--auto [<root>]` | Moves the output the projects under `<root>` (default: the current directory) are about to produce. The workspace runs this on every new clone and working copy |

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

After a stop the symlink stays, but what it points to is gone. af-scratch leaves an existing symlink alone, so to
recreate the directory run `mkdir -p "$(readlink target)"`, or remove the link and run `af-scratch target` again.

## Moving a directory yourself

| The directory | What happens |
|---|---|
| Is already a symlink | Nothing, with a note naming its target |
| Exists | It is moved to the working disk, once (this run is slow: everything is read off the home), then linked |
| Does not exist | An empty directory is made on the working disk and linked |
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

- a symlink is left alone (it may be a link you made, to share with another working copy);
- an existing directory git does **not** ignore is left alone;
- an existing directory git ignores is moved and linked;
- a missing one is linked to a new empty directory, so the first build already writes to the working disk.

A fresh checkout therefore has an empty `target` or `build` link. A check such as `[ -d build ] || …` takes it for a
finished build, so run builds unconditionally.

`node_modules` and `.venv` are never moved by `--auto`. Where the workspace has no working disk, `--auto` exits with
status 0 and does nothing. It always exits with status 0.

## `--status`

```
$ af-scratch --status
/home/dev/repos/shop/target                                  412M
/home/dev/repos/app/build                                    96M
```

It lists directories whose original path is still a symlink, up to six path levels deep. With nothing moved it prints
`逃がしているものはありません。`.

## Environment

| Variable | Effect |
|---|---|
| `AF_WS_SCRATCH` | The working disk. Set by the deployment; when it is unset, af-scratch does nothing |
| `AF_WS_SCRATCH_AUTO=0` | Turns `--auto` off |
| `AF_WS_SCRATCH_AUTO_DEPTH` | How many levels `--auto` looks under `<root>`. Default 3 |

## Exit status

| Status | Meaning |
|---|---|
| 0 | Done. `--auto` always ends here |
| 1 | No working disk (`AF_WS_SCRATCH` is unset), no argument, or a `node_modules` was named (the other directories named are still handled). Also a move or link that failed, with the shell's error, and nothing after it is handled |

## Related

- [Recreate the workspace](../../user/workspace/recreate.md): what survives a stop, a recreate and a clean home
- [af-db](af-db.md): `af-db up --ephemeral` puts a database on the working disk
