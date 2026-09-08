---
name: af-worktrees
description: "Agent Fleet workspace: git worktrees shared with other sessions - which working copy is yours, why the parent clone must never be fast-forwarded or checked out for someone else, integrating the base into your own worktree, the shared stash stack, and sharing node_modules between worktrees without emptying the parent's tree. Read before touching a working copy that is not yours, integrating or fast-forwarding, or installing dependencies in a worktree."
user-invocable: false
---
# Worktrees, the parent clone, and sharing dependencies between working copies

Read when: you are about to touch a working copy that is not the one your session started in,
integrate the base branch, fast-forward anything, share or install `node_modules` in a worktree,
or a git command fails with "checked out at <path>". The prohibitions themselves are in the
always-loaded policy; this is the reasoning and the measured detail.

## Which working copy is yours

A session usually gets `~/repos/<repo>@wip-<slug>`, but can be launched directly in
`~/repos/<repo>`; `git worktree list` and `git -C ~/repos/<repo> status --porcelain` tell you.
In a copy that is not yours, what is forbidden is changing **what it is** — `checkout` / `switch`
/ `branch -f` / `stash`, any merge that could conflict or add a merge commit,
`git worktree remove` / `prune`, deleting it (worktree lifecycle, cleanup and the shelf are the
Console's job).

**Your directory name is not your session name** — a worktree keeps the slug of whichever session
created it. Use `$AF_SESSION_NAME`.

## Your worktree starts at the newest base; the parent clone is a separate question

The worktree is created in the parent with `git worktree add -b <new> <dir> <base>`, where
`<base>` resolves against the parent's **local** refs — which nothing ever moves (auto-fetch only
refreshes `origin/*`). So the Console then fast-forwards **the new worktree** with
`git pull --ff-only origin <base>` from inside it, leaving the parent untouched — skipped
deliberately when the local base is ahead/diverged (your unpushed work is the base you meant) or
origin has no such branch.

**The parent clone therefore stays where it was, and that is fine** — nothing is forked off it
any more. Refresh it only when *it* is what you want current (reading its diff, comparing against
it): `git -C ~/repos/<repo> pull --ff-only`, on a clean tree already on that branch — what the
Console's Fast-Forward on the repo row does. Dirty, or on another branch? Stop and tell the user;
don't "fix" it by checking anything out. **Never fast-forward a parent to "help" someone else's
launch**: `pull --ff-only` aborts only when incoming commits touch a file that copy modified, so
with unrelated edits it succeeds and swaps files out under a working session.

## One object store, one branch per checkout

Worktrees share one object store, so `fetch`, `gc`, tag and branch writes are visible to everyone,
and **a branch can be checked out in only one worktree at a time**. Who holds what is not fixed
(the parent sits on whatever it was last left on) — `git worktree list` tells you.

**Integrate in the right direction and the question stops mattering**: bring the base *into* your
worktree with `git fetch origin && git merge origin/<base>`, which reads the remote-tracking ref
and works whether or not a local branch of that name is checked out anywhere. (The Console's
worktree row also offers Fast-Forward.) Even when the base branch is free, don't check it out
here — your session belongs on its own branch.

Moving a branch another copy holds is refused by git in every form — `checkout`, `branch -f`,
`push . HEAD:<branch>`, `fetch origin <branch>:<branch>` all fail with "checked out at <path>"
(measured). **Never use `--ignore-other-worktrees`**: it is the one thing that succeeds, and then
two copies share one branch ref and silently revert each other's commits.

**To land your work, push your branch** and let the user merge it (PR, or from the Console).
Merging into the parent locally edits another session's checkout, and a conflict there leaves
that session in a broken tree it never asked for.

## The stash stack is shared

The git stash stack is shared with the main checkout and all other worktrees, and other sessions
may push or pop it concurrently. Never use bare `git stash` / `git stash pop` — you could pop
another session's changes. Prefer a temporary WIP commit to set work aside; if you must stash, use
`git stash push -u -m "<unique-tag>"`, capture your entry's SHA via `git stash list --format='%H %gs'`,
restore with `git stash apply <sha>` (not pop), and afterwards drop the entry, re-finding its
current `stash@{n}` by tag first.

## Dependencies in a worktree (disk, not just memory)

N worktrees means N copies of every per-project dependency tree, unless the ecosystem shares one
(Go, Gradle/Maven and Cargo do; **npm does not**).

- **Check the disk before a big install** — `df -h ~`. The volume is shared with everything else
  you do, and caches grow without bound (`~/.npm`, `~/.cache` reach tens of GB).
- **Node is the expensive one** (300 MB+ per worktree). You may share the parent clone's tree by
  symlink when the lockfiles are identical (`cmp -s` them first), but **`npm ci` through that link
  empties the parent's `node_modules`** and breaks every session using it — and
  `rm -rf node_modules/` (trailing slash) deletes through the link the same way. Remove the link
  with `rm -rf node_modules` (no trailing slash) before any install. `npm install <pkg>` replaces
  the link with a real tree: fine, just no longer shared.
- When `$AF_WS_SCRATCH` is set, `node_modules` / `target` / `.venv` / `build` are already symlinks
  into `/scratch` in a fresh checkout — see `/usr/local/share/agent-fleet/notes/environment.md`.
- Go / Gradle / Maven / Cargo already share one cache; nothing to do.
