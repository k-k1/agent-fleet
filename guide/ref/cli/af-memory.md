---
audience: "anyone looking up af-memory's subcommands, options, output and exit codes"
source_of_truth: "af-memory --help, from the workspace-agent in the workspace"
---

# af-memory

English | [日本語](af-memory.ja.md)

Works with Agent Fleet memory from the workspace's terminal: list the changes, import Claude Code's and muse's memory,
write back to Claude Code, lint, and list promotion candidates. Every command asks the workspace's Agent, so the same switch and secret scan as in the Console apply.
The Console flow is in [Import and export memory](../../user/memory/import-export.md).

## Synopsis

```sh
af-memory changes [--limit N]
af-memory import-sources
af-memory import --project <slug> [--dry-run]
af-memory muse-import-sources
af-memory muse-import --dir <name> [--scope project|personal_project] [--dry-run]
af-memory muse-import --scope personal --into-user [--dry-run]
af-memory export-sources
af-memory export --project <id or name> [--dry-run] [--overwrite <name>]...
af-memory lint [--user | --project <id or name>] [--json] [--apply]
af-memory promote-candidates [--project <id or name>] [--days N] [--json]
af-memory --help
```

## Subcommands

| Subcommand | What it does | Writes |
|---|---|---|
| `changes` | Lists the changes to Agent Fleet memory, newest first | No |
| `import-sources` | Lists the Claude projects that can be imported | No |
| `import` | Imports a Claude project's memory | Yes, without `--dry-run` |
| `muse-import-sources` | Lists the muse folders that can be imported | No |
| `muse-import` | Imports muse's notes | Yes, without `--dry-run` |
| `export-sources` | Lists the projects that can be written back to Claude Code | No |
| `export` | Writes a project's memory back into Claude Code's own memory | Yes, without `--dry-run` |
| `lint` | Checks the memories and prints a report | Only with `--apply` |
| `promote-candidates` | Lists the project memories worth proposing to the repository's documents | No |
| `--help` (`-h`, `help`) | Prints the usage | No |

## Exit status

| Status | Meaning |
|---|---|
| 0 | Done |
| 1 | Failed. This includes an import that skipped items and a write-back that could not write a file |
| 2 | Usage error (an unknown subcommand, a missing or extra option). The usage is printed too |

Errors go to standard error as `af-memory: <message>`. An error the Agent returned ends with `(<code>)`.
`cannot reach the Agent on this workspace` means the workspace's Agent could not be reached.

## changes

```sh
af-memory changes [--limit N]
```

Prints up to `N` published changes, newest first. The default is 30.

```
2026-11-10T09:12:03Z  update  acme-shop/test-commands  by claude (s4k2m9x)
2026-11-09T17:40:51Z  create  user/review-style  by member (console)
(1 change(s) withheld: they did not pass the secret scan)
```

Each line holds the time, the operation, `<project>/<name>` (`user/<name>` for user-wide memory), and the author kind and session.
A change that fails the secret scan is not printed; the count is given at the end.

## import-sources, import

Imports the memory the workspace's Claude Code kept for a project into Agent Fleet memory.

```sh
af-memory import-sources
af-memory import --project <slug> --dry-run
af-memory import --project <slug>
```

| Option | Meaning |
|---|---|
| `--project <slug>` | The Claude project to import. Required. Pass the name in the first column of `import-sources` |
| `--dry-run` | Shows what would happen and writes nothing |

`import-sources` prints each Claude project with its file count and the project it imports into. One that cannot be imported shows a reason instead.

```
-home-dev-repos-acme-shop                                      12 files  -> acme-shop
-home-dev-repos-old-tool                                        3 files  no_project
```

`import` first prints the preview by status, then imports the new (`new`) and updated (`update`) items 50 at a time.

| Status | Meaning |
|---|---|
| `new` | Imported as new |
| `update` | The Claude file is newer; the Agent Fleet copy is overwritten |
| `unchanged` | Not changed |
| `forgotten` | A memory you forgot; not imported |
| `secret` | Possible secret; not imported (the file, line and a masked hint are shown) |
| `invalid` | Cannot be imported (the reason is shown) |

```
claude project -> acme-shop
new        2
  test-commands
  deploy-notes
unchanged  10
progress: 2/2
done: 2 imported, 0 skipped
```

- **Importing needs the Agent Fleet memory switch on.** `--dry-run` works while it is off
- An item whose file changed since the preview, for instance, is skipped (`skipped`) with its reason, and the command exits with status 1

## muse-import-sources, muse-import

Imports Muse Code's notes.

```sh
af-memory muse-import-sources
af-memory muse-import --dir <name> [--scope project|personal_project] [--dry-run]
af-memory muse-import --scope personal --into-user [--dry-run]
```

| Option | Meaning |
|---|---|
| `--dir <name>` | The folder to import. Required for `project` and `personal_project`, not given for `personal` |
| `--scope <store>` | `project` (the default), `personal_project` or `personal` |
| `--into-user` | Says you know `personal` lands in your user-wide memory, which every project reads. Required for `personal`, refused for the other stores |
| `--dry-run` | Shows what would happen and writes nothing |

| Store | Reads | Pass to `--dir` | Imports into |
|---|---|---|---|
| `project` | The working copy's `.agents/memory` | The working copy's directory name | That working copy's project |
| `personal_project` | muse's folder per working copy under `~/.local/share/muse/memory/projects` | The name `muse-import-sources` prints | The matching working copy's project |
| `personal` | `~/.local/share/muse/memory/personal` | Nothing | Your user-wide memory |

- The statuses are those of `import`, plus `diverged` (Agent Fleet has its own version; not imported)
- A file git tracks is marked `(shared: git tracks this file, teammates may have it)`
- A note without frontmatter is marked `(no frontmatter: …)`; its name is the file name and its description the first line
- The switch and the secret scan are those of `import`

## export-sources, export

Writes a project's Agent Fleet memory back into Claude Code's own memory, once. The Agent Fleet memory switch is not needed.

```sh
af-memory export-sources
af-memory export --project <id or name> --dry-run
af-memory export --project <id or name> [--overwrite <name>]...
```

| Option | Meaning |
|---|---|
| `--project <id or name>` | The project to write back. Required. Pass the id or the name from `export-sources`; when two projects share a name, pass the id |
| `--dry-run` | Shows what would happen and writes nothing |
| `--overwrite <name>` | Replaces a conflicting (`conflict`) file with the Agent Fleet version. Repeat it as often as needed |

| Status | Meaning |
|---|---|
| `new` | Creates a file Claude does not have |
| `update` | Updates a file written back earlier |
| `remove` | Forgotten in Agent Fleet; removes a file written back earlier |
| `conflict` | A file Agent Fleet did not write, or that changed since. Kept unless named with `--overwrite` |
| `secret` | Possible secret; not written |
| `unchanged` | Not changed |
| `native_only` | Only in Claude; never deleted |

- Claude's memory is snapshotted before anything is written, and `snapshot of claude's memory taken first (<id>)` is printed
- `MEMORY.md` is regenerated, and how many memories it lists is printed
- User-wide memories, and memories limited to other agent kinds, are not written; their counts are printed
- While the switch is on, a note warns that a Claude session started before it was switched on may still be writing its own memory

In these cases the write-back ends with exit status 1.

| Message | What to do |
|---|---|
| `<N> newer edit(s) were kept under hidden names` | An edit made in Claude during the write-back is kept under another name. Move the file named above back by hand |
| `<N> file(s) could not be written` | Some files could not be written |
| `MEMORY.md could not be updated` | `MEMORY.md` changed during the write-back or could not be written. Start again from the preview |

## lint

Checks Agent Fleet memory and prints a report. What it finds is explained on the page about finding memories that went stale.

```sh
af-memory lint [--user | --project <id or name>] [--json] [--apply]
```

| Option | Meaning |
|---|---|
| (no scope) | Checks the user-wide memory and every project that holds memory |
| `--user` | Checks the user-wide memory only |
| `--project <id or name>` | Checks one project only. Not together with `--user` |
| `--json` | Prints the report as JSON |
| `--apply` | Writes what it finds into the memories as reasons ([`--apply`](#write-the-findings)) |

```
== acme-shop: 24 memories, 2 finding(s)  [refs/remotes/origin/main @ 3f9c2a1e5b07]
   missing-path   deploy-notes:4  scripts/deploy.sh
   oversize       architecture  5210 bytes
== user (every project reads it): 6 memories, 0 finding(s)
```

| Finding | Meaning |
|---|---|
| `broken-link` | A `[[link]]` to no memory |
| `missing-path` | A path missing from the default branch |
| `missing-symbol` | A symbol found nowhere in the default branch |
| `missing-adr` | An ADR number with no record |
| `retired-term` | A retired term |
| `oversize` | A body over 4,096 bytes |

- Without `--apply` nothing is written, and reading does not count as a use of a memory
- The repository is read by ref from `origin`'s default branch, never through a working copy. Nothing is fetched, so a stale clone gives a stale answer. In a partial clone (`git clone --filter`) the symbol check is skipped unless the default branch's files are all present (a checkout of it fetches them)
- Proposed retired terms may be listed. Whether one goes on your list is decided in the Console
- The exit status says whether the command worked. It is 0 even when there are findings

### Write the findings

Writes what the report finds into the memories.

- A link, path, symbol, ADR or retired-term finding becomes a reason on the memory, which is then shown as suspect wherever it is read. A size finding does not
- A path that a git rename on the default branch moved is replaced in the body, and only that path. That is its own change, with the operation `lint-rename`
- Each memory changes in one commit authored `lint`. Every one can be reverted from the memory Changes view
- One run writes at most 100 memories. Running it again continues
- Each scope prints `applied (…): N marked, N renamed, N moved since the check, N deferred`, and the report after it shows the state after the writes
- It needs the Agent Fleet memory switch on
- It is refused while a scheduled run or another `--apply` is running. Run it again once that has finished

## promote-candidates

Lists the project memories that have proven themselves enough to be proposed to the repository's own
documents. It only lists them: nothing is written and no text is proposed yet.

```sh
af-memory promote-candidates [--project <id or name>] [--days N] [--json]
```

| Option | Meaning |
|---|---|
| (no project) | Lists every project that holds memory |
| `--project <id or name>` | Lists one project only |
| `--days N` | How long the current revision must have stood unchanged, from 1 to 90 days. Default 7 |
| `--json` | Prints the list as JSON |

A memory is a candidate when all of these hold:

- Its kind is `lesson`, `procedure` or `preference`, and its type is not `user` (it is about the repository, not about you)
- At least two different sessions confirmed its current revision with `memory_feedback`. A confirmation of an earlier revision does not count
- It has stored anchors, and each one is found on the default branch: a file or directory of the tree, an `ADR <n>` with a record, a `[[name]]` of a memory, or a code-shaped symbol found in the files (unqualified: `pkg.Func` cannot be looked up). The whole anchor has to be that one thing: a command, or anything else that cannot be looked up, holds it, even when it mentions a name that exists
- Lint finds nothing about it on the default branch
- It has no open reasons
- Its file has not changed for N days, by the store's history (a reason lint adds or clears is a change too), and it is the same as its last published change

```
== acme-shop: 1 candidate(s), 1 held (confirmed by 2 sessions, unchanged for 7 days)  [origin/main @ 3f9c2a1e5b07]
   candidate  release-steps r3  procedure, 2 sessions, since 2026-11-01T00:00:00Z
              anchors: scripts/release.sh
   held       deploy-notes r1  lesson: no-anchors; confirmations (1 of 2 sessions on revision 1)
```

A memory of those kinds that does not qualify is listed as `held`, with what it misses. The store's history (`changed-recently` by the history, `unpublished`) is read only for a memory that meets every other requirement, so a memory that misses something else may show more once that is fixed:

| Missing | Meaning |
|---|---|
| `about-member` | Its type is `user` |
| `open-reasons` | It is suspect |
| `no-anchors` | It has no stored anchors |
| `unverified` | Something could not be checked, so nothing qualifies: the default branch could not be read or a check over it failed, `origin/HEAD` is not set (the default branch would only be a guess; `git remote set-head origin --auto` sets it), the clone is partial, or the retired-term list cannot be read. The `skipped` lines say which |
| `anchor-not-found` | A stored anchor was not found on the default branch (the anchor is named) |
| `lint` | Lint reports something about it (the finding is named) |
| `moved` | It changed while the list was being made. Run it again |
| `unpublished` | Its file differs from its last published change, for example after an edit outside Agent Fleet, or its revision does not follow the change before it |
| `confirmations` | Too few sessions confirmed its current revision |
| `changed-recently` | Its current revision is younger than N days |

- Archived and superseded memories, and the kinds `state` and `log`, are not listed
- The repository is read the way `lint` reads it: by ref from `origin`'s default branch, never through a working copy and without a fetch
- Reading does not count as a use of a memory. The exit status says whether the command worked
