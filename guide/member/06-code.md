---
audience: "anyone working with repositories, branches and commits"
---

# 06. Repositories and git — cloning, reviewing changes, committing, pushing

English | [日本語](06-code.ja.md)

## Connect a git provider

To clone or push private repositories, first connect GitHub or Bitbucket.
Do this in **⚙Settings → the "Git hosting" tab** (the connection goes through the Agent inside the
workspace, so the workspace must be started).

- **GitHub**: "Connect via OAuth" (recommended; a device flow you approve in the browser) or "Connect with an access token" (paste a Personal Access Token).
- **Bitbucket**: "Connect via OAuth" (recommended; a code grant you approve in a separate tab) or "Connect with an app token" (Atlassian email + API token).

OAuth (device flow) is three steps: **Copy the code** shown, **Open the link and paste**,
then **Wait for approval**. Once connected, your handle and email are displayed.

If your tenant connects GitHub through a **GitHub App**, the card says so: the connection reaches
only the repositories the app is installed on. Install it on your account or organization and
choose the repositories with **"Install the app / choose repositories"**, before or after
connecting; the same link later adds or changes them.

**If "Connect via OAuth" is not offered**, your tenant has no OAuth app for that provider (for
GitHub, a tenant administrator chose none). It is a tenant setting, not something you can enable yourself: ask a tenant
administrator to add it under **Tenant settings → Integrations → Integration OAuth apps**. Connecting
with a token works in the meantime and is not a lesser connection.

After connecting, git authentication applies **transparently** to every git operation. You will
never be asked for a token on each clone or push.

## Clone a repository

Open **"Add"** in the **Repositories** section of the left pane and pick **Git** under **Kind**.
There are two sources.

- **Pick from connections** (default): choose a repository and branch from your connected GitHub / Bitbucket. Private repositories are marked with 🔒. Tabs for unconnected providers cannot be selected and show "Not connected (Settings → Git)".
- **Enter URL**: enter a "Clone URL" (`https://…` / `git@…`) and "Branch (optional)". For repositories you are not connected to.

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
- **Until the first commit exists, a separate working copy (worktree) cannot be created** (git
  cannot resolve HEAD yet), so launches during that window run directly in the folder (the launch
  dialog says so). After one commit it behaves like any other repository.
- The name must start with a letter or number and must not collide with an existing working copy.

### Submodules and Git LFS

- **Submodules**: fetched on a best-effort basis after a clone and after a working copy (worktree) is created. Submodules registered over SSH are automatically rewritten to HTTPS and fetched (even if this fails, the parent clone succeeds).
  - Each working copy clones the submodules again, so **a large submodule may not finish fetching within the launch**. You can still start working (the fetch continues in the background). When work starts on an incomplete checkout, the notification center shows "Started work with submodules not checked out", followed by "Submodules are now checked out" once the fetch lands. Clicking the notification opens that working copy's Source Control view, which lists the submodules and their fetched state.
  - A submodule whose fetch was cut off is repaired automatically on the next launch. If you are in a hurry, enter the working copy in a terminal and run `git submodule update --init --recursive`.
- **Git LFS**: actual content is fetched automatically on clone / checkout (smudge). In an existing working copy, files that are still pointers show an "LFS pointer" badge in the viewer. In that case, enter the repository in a terminal and run `git lfs pull` to fetch the content.

## The built-in git provider (internal Git)

You can also **host repositories inside the fleet** without using external hosting.
In **⚙Settings → the "Internal repos" tab**, just enter
a name and click **"Create"**. No external account is needed, and you can share clone / push with
members in your tenant (authentication is transparent via an auto-injected token). From the row you
can copy the clone URL or **"Browse"** the contents (browsing without cloning). It suits
prototypes and in-team sharing.

- A repository can be **renamed**, and **deleted** when no longer needed (deletion cannot be undone).
  Only **the person who created it or a tenant administrator** can do either; on other rows the two
  buttons are not shown.
- The tab talks to the control plane directly, so **it works while the workspace is stopped**.
- It is also a home for code that must not leave the building. Clone it like any other
  repository ("Start" → "Clone a new repository…") by pasting the URL you copied.

## Launch a session from a repository row

Cloned repositories are listed under **Repositories**. The **"Launch"** button on a row opens
the **"Start working"** screen, where you choose the agent, model, **location**, and the first
prompt, then launch. The location defaults to an isolated **"New worktree"**; what each choice
does, the base branch and branch name, and the working folder are in
[Worktrees and new branches](03-starting-sessions.md#worktrees-and-new-branches-the-key-to-parallel-work).

The **▾** to the right of "Launch" lets you pick a kind (claude / codex / cursor / copilot / kiro / agy / opencode / lcpp / muse / shell) and
**launch instantly** without opening the settings screen (Ctrl / middle-click launches in a new pane).

Rows also show status indicators. Learning to read them helps you catch things before pushing.

- **Uncommitted**: there are changes that have not been committed.
- On a worktree's chip, "parent" is the upstream of the parent working copy's branch (e.g. `origin/develop`). A PR merged on GitHub shows up after the next fetch (automatic, every 10 minutes) without fast-forwarding the parent working copy. With no upstream, the chip compares with the parent working copy's HEAD. The chip's tooltip names the target. Commits the parent working copy has not pushed are not compared and are not brought in by the fast-forward item (they reach the comparison target only once pushed).
- Worktree **= parent**: same commit as the parent.
- Worktree **unmerged N**: there are N commits unique to the worktree not yet in the parent.
- Worktree **parent+N, FF ok**: the worktree's HEAD is contained in the parent, and **the parent is N commits ahead**. **"Fast-forward from origin/develop"** in the right-click menu (**"Fast-forward from parent"** without an upstream) brings those changes straight into this worktree (no merge commit).
- Worktree **diverged N↕M, no FF**: both the worktree and the parent have unique commits; a merge or rebase is needed.
- Worktree **n/a**: the relationship cannot be determined, e.g. detached HEAD or a repository with no commits.
- **↑N** (ahead): N commits ahead of origin (not pushed).
- **↓N FF ok**: origin is N commits ahead and can be fast-forwarded cleanly (Fast-Forward in the commit graph).
- **↑N ↓N needs merge**: diverged from origin. A fast-forward is not possible; a merge or rebase is needed.

Worktree status indicators compare against the current branch in the parent working copy, while
`↑` / `↓` compare against origin. Hover over the status indicator to see what it is compared
against and each side's unique commit counts. Because a squash merge does not preserve commit
ancestry, a branch merged on the hosting service may still not show as **merged**.

The `●N` at the end of a row is the number of running sessions; a plain number badge is the number
of stopped sessions. When a repository is collapsed, this includes sessions of its worktrees. A
colored number is the pane number showing that repository's commit graph. See
[Icons, badges, and menus](badges-and-menus.md) for details.

### First-prompt templates

The **"Templates"** button above the first prompt opens a searchable list of three kinds of
starting text:

- **My templates**: your own. **"New template"** creates one (name, prompt, and where it shows:
  every repository, or only this one). Pick one and use **"Edit"** to change or rename it, or
  **"Delete"** to remove it.
- **This repository**: the entries of `.agent-fleet/launch-prompts.md` committed in the
  repository (one per `## heading`), shared with everyone who clones it. Read-only here.
- **History**: the first prompts you launched in this repository recently (8 at most), shown by
  their first line. **"Delete"** forgets one; **"Save as template"** keeps it as one of yours.

Type to filter, **↑ / ↓** to move, **Enter** to insert, **Esc** to close. The highlighted entry's
full text shows in the preview beside the list (on a phone, the first tap previews and
**"Insert"** inserts). `{{repo}}`, `{{branch}}` and `{{path}}` are filled in for the row you
launch from. If the first prompt already has text, you are asked whether to **insert at the
cursor** or **replace all**; typed text is never dropped silently.

Your templates and history are saved with your settings, so they follow you to every browser and
device. They have a size budget (40 templates, 16 KB in total, 8 KB each); a prompt larger than
3 KB (or one that would push your synced settings past their 64 KB limit) is kept in history on
that device only. To invoke a repository's `.claude` commands or
skills, use the skill button (**/**) next to "Templates" instead; they are no longer listed here.

### What you can do with right-click

Right-clicking a repository or worktree row shows the following actions. Some items are hidden
depending on state and location.

- **Open commit graph** / **Open the folder** / **Commit changes**
- **Switch branch** / **Copy the branch name** / **Copy the directory name** / **Fast-Forward** (on a worktree, **"Fast-forward from origin/develop"** or similar)
- **Initialize Git Flow** (a git repository's base copy only); see [below](#initialize-git-flow)
- **Skills…**: the skills this repository gives each agent, with where they live and which copy wins ([09](09-agents.md))
- **Project settings**: the MCP definitions committed in that repository, with per-agent status and warnings ([20](20-settings.md#mcp-servers-used-by-sessions))
- **Share…**: share this working copy's (project's) sessions with another member ([12](12-collaboration.md#sharing-a-conversation-shared-sessions))
- **Assignment to a working set** ([11](11-many-sessions.md#narrowing-the-view-with-working-sets))
- **Launch shell**: opens a shell session in that copy (the other kinds launch from the **▾** next to "Launch")
- **Start an image studio**: begins an image studio in that copy ([13](13-images.md#studio-refine-the-draft-by-talking-to-an-agent)); shown only while the workspace is running and image generation is available
- **Stop the sessions below**: stops every session running in that copy and in the copies nested under it, from one modal ([05](05-stopping-and-tidying.md#stopping-and-tidying-up-sessions)); shown while any is running, with the count
- **Archive all stopped sessions**: archives every stopped session of that copy in one go; shown while any is stopped, with the count ([05](05-stopping-and-tidying.md#stopping-and-tidying-up-sessions))
- **Archived sessions of this working copy**: opens the archive list narrowed to that copy, to restore or delete from
- **Lock against deletion** / **Unlock (allow deletion)**: marks the working copy (worktrees included) as one that must not go away. A lock icon appears on the row and automatic tidying skips it ([05](05-stopping-and-tidying.md#locking-something-against-deletion)); **Delete the working copy** stays greyed out until you unlock
- **Delete the working copy** (only for working copies that can be deleted; [05](05-stopping-and-tidying.md#clearing-one-finished-job-deleting-a-working-copy))

### Initialize Git Flow

git-flow keeps its settings in `.git/config`, which a clone does not copy, so a repository that
runs git-flow usually declares nothing in the copy Agent Fleet made. **Initialize Git Flow** writes
the settings `git flow init` would write, so that new branches for issues start from the
development branch with git-flow's prefixes.

- **Fields:** production branch, development branch, feature / release / hotfix prefix, version
  tag prefix, and an optional bugfix prefix. They are filled in from the settings already present,
  the repository's Bitbucket branching model when it declares something, and which of `develop`,
  `main` and `master` exist.
- **What it writes:** the `gitflow.branch.*` and `gitflow.prefix.*` keys, into the base copy's git
  config. `support/` is added only when it is not set, and an empty bugfix prefix leaves that
  setting as it is. Nothing is committed and nothing is checked out.
- **Shared:** every worktree and session of the repository reads these settings at once. Settings
  that already exist and would change are listed before you save, and the button reads
  **Overwrite**. If someone changes them while the dialog is open, saving is refused and the
  dialog reloads.
- **Refused** when the production or development branch exists neither locally nor on `origin`.
- **Only on origin:** the `git flow` command and Fork want the production and development branches
  locally, so saving creates a local branch tracking the one on `origin` (like
  `git branch --track develop origin/develop`). The dialog says so under the field, and the message
  after saving names the branches it created. It never checks one out, and it never moves a local
  branch that already exists, even when it differs from `origin`'s. If the branch is created but its
  upstream cannot be set, saving again will not fix that; the dialog shows the
  `git branch --set-upstream-to=…` command to run instead.
- A committed `.agent-fleet/branches` or `.gitflow` takes precedence over these settings where the
  two overlap, and the dialog says so.
- When you start work from an issue in a repository whose `origin` has `develop` and that declares
  nothing, the launch dialog offers **Initialize Git Flow…**. It never initializes by itself.

## Commit in the commit graph view

A normal click on a repository row expands / collapses the sessions and worktrees underneath it.
The **commit graph** view opens from "Open commit graph" in the right-click
menu. Ctrl / ⌘+click or middle-click opens it directly in a new pane. The header shows the
current branch and action buttons, which collapse into **⋯** when space is tight.

- **Changes**: opens the work screen for committing changes (in a separate pane).
- **fetch**: fetches from the remote (`git fetch --prune`).
- **Fast-Forward**: fast-forwards the current branch to upstream (`pull --ff-only`).
- **Refresh**: refreshes the display.
- Clicking the branch name part opens **"Switch branch"** (with filtering, sorted by latest commit).
- If a `.gitmodules` file exists, **submodules** are listed in the header's target selector.
  Selecting a fetched submodule lets you browse that submodule's own commit graph and commit
  details. Unfetched submodules are also listed as "(not fetched)", but they cannot be selected
  until they are initialized.

### Changes → stage → commit

Opening "Changes" shows the list of changed files (`{repository name} — Changes`).

- Use the **stage** / **unstage** buttons on each row to move files in and out. For tracked files you can also **Discard changes** (with a "This cannot be undone." confirmation).
- Write in the **Commit message** field below, check **"Stage all tracked (-a)"** if needed, then click **"Commit"**. If the message is empty, "A commit message is required" is shown.
- The commit author (identity) can be overridden from this screen. The resolution order is "repo override > provider > global default" (also configurable in ⚙Settings → "Git hosting").

### History and diffs

Clicking a row in the commit graph opens that **commit's details** (changed files and diff).
Diffs can be folded per file, and you can adjust how they are shown with "Expand all",
"Collapse all", and "Wrap". Right-clicking a commit on the graph offers "Show details",
"Switch branch", "New branch from this commit…", and more.

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

- **A specific path only / multiple paths**: you can check out just a subtree via the subpath.
  Checking out another path again creates a separate folder, giving you the same isolation as
  git's separate clones. Since SVN has no worktrees, **this is how you split parallel work**
  (session launch from an svn row is **in-place only**; no worktree option is shown).
- **Saving credentials (optional)**: if you check the save option, credentials are stored in
  the encrypted store and reused automatically for subsequent updates. The password never
  appears in the process list or in a plaintext cache.
- **Entering credentials later**: you do not have to check out again if you declined. Use
  **"Re-authenticate (svn)"** on the row menu (an update that fails on authentication offers it
  by itself). What you enter is checked against the server before it is saved. Servers saved this
  way are listed under Settings › Connections › Git hosting, where you can also add or forget one.
- **Self-signed certificates**: if the certificate cannot be trusted (e.g. an in-house server),
  turn on "Trust self-signed certificate" in the modal. This is a per-server opt-in that
  **disables certificate verification for that server**, and it persists across future updates.
- **Update and lock cleanup**: use "Update (svn)" on the svn row to move to the latest revision.
  If the working copy gets locked (an error prompting `svn cleanup`), e.g. after an interruption,
  checkout / update automatically attempts one recovery. If the lock remains, use
  **"Clean up lock (svn)"** from the row menu.
- **Show log and local changes**: the svn row menu has **"Show log"** (revision list, newest
  first, with a path filter and "Load more"; a revision newer than the working copy is marked
  "not updated yet", and selecting one shows its changed paths and diff) and **"Local changes"**
  (the entries of `svn status`; a click opens the file's diff). Right-click a folder or file
  under the working copy in the Files tree and choose **"Show log"** to filter the log to it.
  The log is read from the server when you open it or press "Load more" (it is not refreshed
  in the background), and if the server needs a password you get the re-authenticate dialog.
  Both views are read-only.
- The svn row shows the current revision (`r1234`). Branch switch and the stage / commit
  workbench are git-only, so commit with `svn commit` inside a session. **The saved
  credentials apply there too**, so `svn update` / `svn commit -m "…"` typed in a session
  authenticate without a `--username`. Two cases still ask for the password themselves: a
  `commit` with no `-m` (svn opens an editor and keeps the terminal), and a command you gave an
  explicit `--username` / `--password`; an explicit credential always wins.
