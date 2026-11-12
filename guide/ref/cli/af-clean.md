---
audience: "anyone looking up af-clean's options, output and exit codes"
source_of_truth: "af-clean --help, from the workspace-agent in the workspace"
---

# af-clean

English | [日本語](af-clean.ja.md)

Frees the home disk from a terminal, with the same list the Console's low-disk cleanup shows (**Clean regenerable items…**
on the "home disk is nearly full" notice). It asks the workspace's Agent what can go and removes only that. It works
inside a running workspace only. The Console side is in [See your machine and its usage](../../user/workspace/machine.md#free-up-the-home-disk).

## Synopsis

```sh
af-clean [--dry-run] [--json]
af-clean --apply [--json]
af-clean --help
```

| Option | What it does |
|---|---|
| (none), `--dry-run` | Lists what would be removed. Nothing is deleted |
| `--apply` | Builds a fresh list, then removes those items |
| `--json` | Prints one JSON document instead of text |

`--apply` and `--dry-run` cannot be combined.

## What it offers

| Kind | What goes | Offered when |
|---|---|---|
| `worktree` | `node_modules`, `.next`, `target` and `.gradle` of a working copy (the working copy stays) | Nobody touched it for 30 days, it has no uncommitted or untracked change, and no session or process uses it |
| `cache` | A package-manager cache (go-build, npm, uv, pip, Maven, Gradle versions no project pins) | It holds something, no build or install uses it, and the Agent offers it |

No other kind of item is ever removed from here. The trash, session archives, leftover files, `~/.af-work` and the whole-home
breakdown are shown in the Console only, each behind its own confirmation.

Removal **is not reversible and does not go to the trash**. The next build or install that needs a removed item is slower, once.

## How `--apply` stays inside the list

- It deletes the items of its own fresh list, one request each, and asks nothing the Console does not. It does not reuse an earlier `--dry-run`: the list is read again on every run, so it can differ from what a previous dry run showed.
- Right before it cleans a working copy it reads that working copy's list again. If the folders differ from what was listed, the item is skipped (`skipped`, "changed since it was listed") and nothing in it is deleted. Run `af-clean` again to see the new list.
- The Agent's delete names only the working copy. A folder such as `.next` that appears **after** that last check and before the delete is removed with it, as in the Console.
- What the Agent still refuses (a live session, a cache in use) is `skipped`, not a failure. A re-check that cannot be done (the Agent is gone, an HTTP error) is `failed`, and nothing is deleted for that item.

## Output

```
worktree  my-app-feature-x                    1.2 GiB
            node_modules                      1.1 GiB
            .next                           100.0 MiB
cache     npm                               800.0 MiB
dry run: 2.0 GiB can be freed; nothing was deleted. Run af-clean --apply to list again and remove these items.
```

A size with `+` is a lower bound (the measurement stopped at its budget). With `--apply` each line ends in
`removed (<size>)`, `skipped` or `failed`, followed by `freed <size>, <n> skipped, <n> failed`.

`--json` prints `mode`, `items` (`id`, `kind`, `name`, `bytes`, `dirs`, and with `--apply` `outcome`, `freed`, `detail`),
`total_bytes`, and with `--apply` `freed_bytes`, `skipped` and `failed`. `partial` is true when one of the two sources could not be read.

## Exit status

| Status | Meaning |
|---|---|
| 0 | Done (skipped items do not count as failures) |
| 1 | An item failed, or the Agent could not be asked or answered nothing usable |
| 2 | Usage error. The usage is printed too |

Errors go to standard error as `af-clean: <message>`. `cannot reach the Agent on this workspace` means the command was not run inside a running workspace.
