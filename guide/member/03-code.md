# 04. Repositories and git — cloning, reviewing changes, committing, pushing

English | [日本語](03-code.ja.md)

Audience: anyone working with repositories, branches and commits
Source of truth: the Console itself — if a screen disagrees with this page, the screen is right
Updated: 2026-08

> Audience: members who clone repositories and work with git. Covers connecting a git provider,
> cloning, the built-in git provider, launching from a repository row, committing in the source
> control view, and push with authentication.

## Connect a git provider

To clone or push private repositories, first connect GitHub or Bitbucket.
Do this in **⚙Settings → the "Git hosting" tab** (the connection goes through the Agent inside the
workspace, so the workspace must be started).

- **GitHub** — "Connect via OAuth" (recommended; a device flow you approve in the browser) or "Connect with an access token" (paste a Personal Access Token).
- **Bitbucket** — "Connect via OAuth" (recommended; a code grant you approve in a separate tab) or "Connect with an app token" (Atlassian email + API token).

OAuth (device flow) is three steps: **Copy the code** shown, **Open the link and paste**,
then **Wait for approval**. Once connected, your handle and email are displayed.

**If "Connect via OAuth" is not offered**, your tenant has not registered an OAuth app for that
provider. It is a tenant setting, not something you can enable yourself: ask a tenant
administrator to add it under **Tenant settings → Integrations → Git provider OAuth**. Connecting
with a token works in the meantime and is not a lesser connection.

After connecting, git authentication applies **transparently** to every git operation. You will
never be asked for a token on each clone or push.

## Clone a repository

Open **"Add"** in the **Repositories** section of the left pane and pick **Git** under **Kind**.
There are two sources.

- **Pick from connections** (default) — choose a repository and branch from your connected GitHub / Bitbucket. Private repositories are marked with 🔒. Tabs for unconnected providers cannot be selected and show "Not connected (Settings → Git)".
- **Enter URL** — enter a "Clone URL" (`https://…` / `git@…`) and "Branch (optional)". For repositories you are not connected to.

If you specify **"New branch (optional)"**, a new branch is created from the base branch and
checked out. When you do, you can also give the working copy its own folder via **"Folder name"**.
Finally, click **"Clone"** to fetch it.

## Starting with nothing to import from (a new folder)

When you are starting something that does not exist anywhere yet, there is nothing to clone.
Use **"Add" → Kind "New folder"** to create an empty folder under `~/repos`. The same thing is
available from **"+ Start" → "Start in a new folder…"**, which continues straight into the
**Start work** dialog once the folder exists.

- The folder is **`git init`ed** as it is created. That is what makes it a row in the left pane,
  with the usual review / commit / share / delete actions available. **A remote can be added
  later** with `git remote add` in a terminal (creating a home for it in the internal git
  provider is one way to do that).
- **Until the first commit exists, a separate working copy (worktree) cannot be created** — git
  cannot resolve HEAD yet — so launches during that window run directly in the folder (the launch
  dialog says so). After one commit it behaves like any other repository.
- The name must start with a letter or number and must not collide with an existing working copy.

### Submodules and Git LFS

- **Submodules** — fetched on a best-effort basis after a clone and after a working copy (worktree) is created. Submodules registered over SSH are automatically rewritten to HTTPS and fetched (even if this fails, the parent clone succeeds).
  - Each working copy clones the submodules again, so **a large submodule may not finish fetching within the launch**. You can still start working (the fetch continues in the background). When work starts on an incomplete checkout, the notification center shows "Started work with submodules not checked out", followed by "Submodules are now checked out" once the fetch lands. Clicking the notification opens that working copy's Source Control view, which lists the submodules and their fetched state.
  - A submodule whose fetch was cut off is repaired automatically on the next launch. If you are in a hurry, enter the working copy in a terminal and run `git submodule update --init --recursive`.
- **Git LFS** — actual content is fetched automatically on clone / checkout (smudge). In an existing working copy, files that are still pointers show an "LFS pointer" badge in the viewer. In that case, enter the repository in a terminal and run `git lfs pull` to fetch the content.

## The built-in git provider (internal Git)

You can also **host repositories inside the fleet** without using external hosting.
In **⚙Settings → the "Internal repos" tab**, just enter
a name and click **"Create"**. No external account is needed, and you can share clone / push with
members in your tenant (authentication is transparent via an auto-injected token). From the row you
can copy the clone URL or **"Browse"** the contents (browsing without cloning). Good for
prototypes and in-team sharing.

- A repository can be **renamed**, and **deleted** when no longer needed (deletion cannot be undone).
- The tab talks to the control plane directly, so **it works while the workspace is stopped**.
- It also serves as a home for code that must not leave the building. Clone it like any other
  repository — "Start" → "Clone a new repository…" — by pasting the URL you copied.

## Launch a session from a repository row

Cloned repositories are listed under **Repositories**. The **"Launch"** button on a row opens
the **"Start working"** screen, where you choose the agent, model, **location**, and the first
prompt, then launch. For **location**, the default is **"New worktree"** (isolated · safe across
branch switches); the other option is **"Directly in this copy"**.

- **New worktree** (default) — carves out an independent working copy dedicated to that task. Since edits never collide with other sessions, this is the safe choice for parallel work (you can pick the base branch and branch name; if left empty, a provisional name `temp/…` is used).
- **Directly in this copy** — works directly in the folder currently open.

The **▾** to the right of "Launch" lets you pick a kind (claude / codex / cursor / copilot / kiro / agy / opencode / shell) and
**launch instantly** without opening the settings screen (Ctrl / middle-click launches in a new pane).

Rows also show status indicators. Learning to read them helps you catch things before pushing.

- **Uncommitted** — there are changes that have not been committed.
- Worktree **= parent** — same commit as the parent working copy.
- Worktree **unmerged N** — there are N commits unique to the worktree not yet in the parent.
- Worktree **parent+N, FF ok** — the worktree's HEAD is contained in the parent, and **the parent is N commits ahead**. **"Fast-forward from the parent"** in the right-click menu brings those changes straight into this worktree (no merge commit).
- Worktree **diverged N↕M, no FF** — both the worktree and the parent have unique commits; a merge or rebase is needed.
- Worktree **n/a** — the relationship cannot be determined, e.g. detached HEAD or a repository with no commits.
- **↑N** (ahead) — N commits ahead of origin (not pushed).
- **↓N FF ok** — origin is N commits ahead and can be fast-forwarded cleanly (Fast-Forward in the commit graph).
- **↑N ↓N needs merge** — diverged from origin. A fast-forward is not possible; a merge or rebase is needed.

Worktree status indicators compare against the current branch in the parent working copy, while
`↑` / `↓` compare against origin. Hover over the status indicator to see what it is compared
against and each side's unique commit counts. Because a squash merge does not preserve commit
ancestry, a branch merged on the hosting service may still not show as **merged**.

The `●N` at the end of a row is the number of running sessions; a plain number badge is the number
of stopped sessions. When a repository is collapsed, this includes sessions of its worktrees. A
colored number is the pane number showing that repository's commit graph. See
[Icons, badges, and menus](badges-and-menus.md) for details.

### What you can do with right-click

Right-clicking a repository or worktree row shows the following actions. Some items are hidden
depending on state and location.

- **Open commit graph** / **Open the folder** / **Commit changes**
- **Switch branch** / **Copy the branch name** / **Fast-Forward** (on a worktree, **"Fast-forward from the parent"**)
- **Project settings** — the MCP definitions committed in that repository, with per-agent status and warnings ([12](12-settings.md#mcp-servers))
- **Share…** — share this working copy's (project's) sessions with another member ([02](02-sessions.md#sharing-a-conversation-shared-sessions))
- **Assignment to a working set** ([02](02-sessions.md#narrowing-the-view-with-working-sets))
- Per-kind session launch: claude, codex, opencode, shell, and so on
- **Delete the working copy** (only for working copies that can be deleted)

## Commit in the commit graph view

A normal click on a repository row expands / collapses the sessions and worktrees underneath it.
The **commit graph** view opens from "Open commit graph" in the right-click
menu. Ctrl / ⌘+click or middle-click opens it directly in a new pane. The header shows the
current branch and action buttons, which collapse into **⋯** when space is tight.

- **Changes** — opens the work screen for committing changes (in a separate pane).
- **fetch** — fetches from the remote (`git fetch --prune`).
- **Fast-Forward** — fast-forwards the current branch to upstream (`pull --ff-only`).
- **Refresh** — refreshes the display.
- Clicking the branch name part opens **"Switch branch"** (with filtering, sorted by latest commit).
- If a `.gitmodules` file exists, **submodules** are listed in the header's target selector.
  Selecting a fetched submodule lets you browse that submodule's own commit graph and commit
  details. Unfetched submodules are also listed as "(not fetched)", but they cannot be selected
  until they are initialized.

### Changes → stage → commit

Opening "Changes" shows the list of changed files (`{repository name} — Changes`).

- Use the **stage** / **unstage** buttons on each row to move files in and out. For tracked files you can also **Discard changes** (with a "This cannot be undone." confirmation).
- Write in the **Commit message** field below, check **"Stage all tracked (-a)"** if needed, then click **"Commit"**. If the message is empty, "A commit message is required" is shown.
- The commit author (identity) can be overridden from this screen. The resolution order is "repo override > provider > global default" (also configurable in ⚙Settings → "Git").

### History and diffs

Clicking a row in the commit graph opens that **commit's details** (changed files and diff).
Diffs can be folded per file, and you can adjust how they are shown with "Expand all",
"Collapse all", and "Wrap". Right-clicking a commit on the graph offers "Show details",
"Switch branch", "New branch from this commit…", and more.

## Deleting a working copy

When you no longer need a working copy, use **"Delete the working copy"** from the repository
row's right-click menu or from the delete action in the commit graph header. Only the local
working copy is removed; history and the remote remain. If there are uncommitted / unpushed
changes, a second confirmation ("Force delete") warns you that they will be lost.

## Push and authentication

Push works as a normal git operation (`git push` from a terminal or an agent). If you are
connected, **authentication is transparent and automatic**, so no token entry is needed.
Bitbucket tokens are refreshed automatically even after they expire.

> Uncommitted changes and unpushed branches exist **only inside the workspace**.
> Push work you want to keep frequently ([01 First day](01-first-day.md)).

## Subversion (SVN) repositories

You can work with **SVN** repositories, not just git. In the clone modal, use the **Git / SVN
toggle** to select SVN, then enter the **Repository URL** and, if needed, a **subpath**
(e.g. `trunk`, `branches/x`) and **username / password** (basic auth) to check out.

- **A specific path only / multiple paths** — you can check out just a subtree via the subpath.
  Checking out another path again creates a separate folder, giving you the same isolation as
  git's separate clones. Since SVN has no worktrees, **this is how you split parallel work**
  (session launch from an svn row is **in-place only**; no worktree option is shown).
- **Saving credentials (optional)** — if you check the save option, credentials are stored in
  the encrypted store and reused automatically for subsequent updates. The password never
  appears in the process list or in a plaintext cache.
- **Self-signed certificates** — if the certificate cannot be trusted (e.g. an in-house server),
  turn on "Trust self-signed certificate" in the modal. This is a per-server opt-in that
  **disables certificate verification for that server**, and it persists across future updates.
- **Update and lock cleanup** — use "Update (svn)" on the svn row to move to the latest revision.
  If the working copy gets locked (an error prompting `svn cleanup`), e.g. after an interruption,
  checkout / update automatically attempts one recovery. If the lock remains, use
  **"Clean up lock"** from the row menu.
- The svn row shows the current revision (`r1234`). Branch switch and the commit graph view
  (stage / commit) are git-only, so commit with `svn commit` inside a session. **Saved
  credentials are not passed through to svn commands inside sessions**, so add `--username`
  when needed.
