---
audience: "anyone starting and following AI conversations"
---

# 03. Starting a session — kinds, execution method, the Start dialog, the issue tracker

English | [日本語](03-starting-sessions.ja.md)

A session bundles one job you delegate to the AI into a single unit: its **conversation,
working location, and execution state**. It is separate from whether a terminal
exists: most kinds also run as sessions under managed execution, without a black screen (which kind
supports what: [Agents — what each kind can do](../ref/agents.md)). In the left pane, sessions
appear under the **repository** that matches their working location; those that don't belong to
a repository appear under **Other sessions**. You can have multiple sessions in parallel, each
with its own independent conversation and working folder.

## Session types

- **claude**: launches Claude Code.
- **codex**: launches Codex.
- **cursor**: launches Cursor (needs a Cursor plan; appears once connected; [09](09-agents.md)).
- **copilot**: launches GitHub Copilot (rides on the GitHub connection; [09](09-agents.md)).
- **kiro**: launches Kiro (needs a device-flow sign-in; appears once connected; [09](09-agents.md)).
- **agy**: launches Antigravity (experimental slot; appears once connected).
- **opencode**: launches OpenCode.
- **lcpp**: the fleet's own llama.cpp engine, run as a Managed session (no sign-in; it is in "Start"
  while **"Use llama.cpp"** is On in ⚙ Settings → Agents; [09](09-agents.md#lcpp)).
- **muse**: launches Muse Code as a Managed session (appears once Muse Code is installed and you are
  signed in; [09](09-agents.md#muse-code)).
- **shell**: a plain shell (bash). Opens right away from "Start".

claude / codex / cursor / opencode / copilot / kiro / agy appear in "Start" once you connect the corresponding
agent (connections: [09 Agents](09-agents.md)). lcpp and muse have no terminal of their own and always
run Managed. (**ssm**, which logs in to another host, is
covered in [16 Cloud accounts](16-cloud-accounts.md).)

## Execution method — Managed and Terminal (CLI)

On the start screen of a kind that supports both routes you can choose the
**execution method**; a kind that has only one offers no choice (the per-kind table is in
[Agents — what each kind can do](../ref/agents.md)). This is the
difference in the path Agent Fleet uses to run the agent and deliver your instructions
(internally, the "driver"). It chooses **how a session of the same kind is run**, and does not
give the conversation a separate storage location or a separate working folder.

- **Managed (recommended, default)**: Agent Fleet controls the agent directly.
  You operate it through the chat view; there is no terminal. Codex / opencode run on a shared
  execution runtime and have no per-session CLI process, so they save memory and suit
  parallel work (GitHub Copilot, cursor, and Kiro run a dedicated per-session process even when managed,
  so their memory use is on par with Terminal (CLI)). lcpp runs inside the workspace's Agent with no
  process of its own; muse runs one process per session.
- **Terminal (CLI)**: launches the agent's CLI per session, and you can operate its
  interactive screen directly from the terminal. Suited to cases that need CLI-specific screens
  or commands; each session uses extra memory.

A kind that supports both starts Managed by default. For those kinds you can switch the execution method from the session's ⋯
menu whenever the agent is not in the middle of processing. **The conversation carries over as
is.** A codex session goes from Managed to Terminal (CLI) only while it is stopped: stop it first,
then switch. codex lets go of the conversation about a minute after the stop; until then the
switch, and a resume on Terminal (CLI), is refused with a message to try again shortly. You can
also open the chat view from Terminal (CLI), but managed execution has no terminal screen.

A Terminal (CLI) screen is kept alive behind the scenes even if you close the browser. You
never need to operate that keep-alive mechanism yourself.

## Creating a new session — "Start" at the top of the screen

**"+ Start"** in the workspace action bar is the entry point for launching. Pressing it opens the
**"Start"** screen, where you begin by choosing **where to work**.

- **Chat (assistant)**: starts a simple chat that doesn't use a repository
  ([10 Chat and memos](10-chat-memo.md)).
- **Launch an agent in a repository**: search for and pick a cloned repository, and you move
  straight on to **"Start working"** (agent, model, worktree, first prompt). This is the same
  screen as "Launch" on a repository row ([06](06-code.md#launch-a-session-from-a-repository-row)).
- **Clone a new repository…**: sits below the repository list. Clone by picking from a
  connection or entering a URL by hand; when it completes you continue straight into
  "Start working" (you can specify a new branch, and the worktree folder name is generated
  automatically).
- **Launch an agent in home**: runs an agent in home (~).
  For drafts, research, and throwaway work.
- **shell**: pressing it opens bash immediately.
- **SSM — log in to another host**: shown when SSM is configured; pick the host to log in to
  and connect.

**"Back to Start"** at the bottom left of each step (or the browser back button) takes you one
step back. Sessions are named automatically (repository name + timestamp) and can be renamed
later (see below).

You can press "Start" even while the workspace is stopped. After you confirm, it **starts the
workspace, and the "Start" screen opens automatically once it's ready**, so there is no need to wait for
startup and press the button again.

### Worktrees and new branches — the key to parallel work

When you want to advance multiple tasks in the same repository at the same time, a single
working folder makes them trample each other. By default, Agent Fleet prevents this by carving
out an independent working copy (worktree) per session.

- In **"Start working"** (opened from "Start" or from "Launch" on the base repository), the
  **"Location"** row states in one line where the session will run. Open it with **"Change"**
  to choose between **"New worktree"** (default) and **"Directly in this copy"**.
- With "New worktree" you specify the base branch and an optional branch name. If the branch
  name is empty you get a provisional name `temp/…`; if you enter one, the worktree folder
  name is generated automatically from the branch name.
- **The start point is brought up to origin's tip.** Right after the worktree is created, it is
  fast-forwarded to `origin/<base branch>`, inside the new worktree only. A working copy's
  local branches are never moved by a fetch, so without this a session forked from an old clone
  silently starts weeks behind. **The parent working copy is left alone** (someone may be
  working in it, and files must not be swapped out under them). When the local base is ahead or
  has diverged, or origin has no branch of that name, the local base is used as before.
- Launching from an existing worktree row launches directly in that worktree, and says so in
  place of the choice. Create new worktrees from the base repository. The same happens for a
  Subversion working copy, and for a repository with no commit yet (a worktree needs a first
  commit to branch from).

### Working folder — starting inside a subfolder

**"Working folder"**, under **"Location"** in "Start working", narrows where the agent starts: leave it empty to start
at the working copy root (the default), or pick a folder beneath it, which is useful in a monorepo where
a task only concerns `console/` or `apps/web`. Type the path or press **"Browse"** to walk the
repository's folders.

- With "New worktree" the same relative path is resolved **inside the newly created worktree**,
  not in the base repository. If that branch does not have the folder, the launch is refused and
  nothing is created.
- The session still belongs to the working copy: it stays grouped under the same repository, and
  branch switching, cleanup and worktree deletion behave exactly as before.
- The last folder you launched in is remembered per repository and pre-filled next time.

The standalone "Add" under **Repositories** in the left pane also lets you specify a different
folder name when you specify a new branch or when a working copy with the same name already
exists ([06](06-code.md)). This is a separate path from creating a worktree in "Start working".

## Starting from the issue tracker

The **Issue tracker** section of the left pane lists your work items (Jira issues, GitHub issues
and pull requests, Bitbucket pull requests) so that a session can start with the ticket's context
already in place. It is drawn **whether or not the workspace is running**, because deciding what to
work on happens before a session exists, which is exactly when the workspace tends to be stopped.

**Nothing is listed until you register a saved query** (⚙ Settings →
[Issue tracker](20-settings.md#issue-tracker)). That query is the whole of the filtering; nothing is
synced in bulk.

### Reading the list

- The count beside the section name is the **unfinished** items, and the line under it says **when
  the list was last fetched**. While the workspace is stopped it says so as well: the list stays
  readable, but nothing here starts your workspace just to draw a list.
- A long list **folds at 10 rows**, with "Show more (N left)" underneath; the count keeps counting
  all of them. Past ten rows you also get a **one-line filter**, which searches the rows already on
  screen (key, title, assignee, labels, repository); **×** or Escape clears it. When it finds
  nothing, or a query fetched only part of its matches, **Search the tracker** asks the tracker
  with the words you typed.
- An open GitHub pull request's row shows its **CI** (failing, running or passing) and a mark
  when it **conflicts** with its base, as of the last fetch. When GitHub has no answer yet, nothing
  is drawn.
- A row carries **only what differs between rows**: when every row of a query has the same assignee
  or repository, that line is dropped. A relative time appears only on rows that have **not moved
  for a day or more**; for the rest, being near the top already says it.
- A row that changed since you last looked gets a **dot and a bold title**; its tooltip says whether
  it was updated or got new comments, and the line under the section name counts them ("N
  updated"). GitHub and Bitbucket rows also show their **comment count**. What you have seen is
  kept per browser, opening an item's details counts as seeing it, and an item listed for the first
  time is never marked.
- **🔗** opens the item in its own tracker. A **dot badge** means somebody has already started this
  one, and clicking it opens that session. It is there to stop a second person *before* the launch.
- The details list **Sessions already on this** by name. One that has since been archived can be
  restored from there (it asks first; if its working folder is gone it restores but cannot resume),
  and one that was deleted says so.

### From a row to a session

Clicking a row opens its details: state, kind, provider, assignee, repository, labels, when it last
moved, and a link to the original page. **The description is not shown here.** A body is read inside
the session, where `gh` and the `jira` CLI can fetch it, and the list has to work while the
workspace is stopped.

- Pick the **repository** and **where to work** (a new worktree, the base copy itself, or an
  existing worktree), then **Start**. A ticket does not know which working copy its work happens
  in: a GitHub item names a repository but not a copy, and a Jira issue names neither.
- The usual launch dialog then opens, with the agent, model and first instruction filled in. **That
  instruction carries the key, the title, the URL and where to read the body**, not the body
  itself.
- For an issue, the launch dialog is filled in with a branch name and a base branch from the
  branch naming rules: by default the issue type or labels pick the prefix, then the number and
  the title, e.g. `feature/45-empty-list` or `fix/PROJ-123`. A repository that declares git-flow
  or a Bitbucket branching model gets its own prefixes and base, and the dialog says where the
  base came from. A repository whose `origin` has `develop` but declares nothing gets an offer to
  [initialize Git Flow](06-code.md#initialize-git-flow). A name outside the rules only gets a note; nothing is refused. The template is
  in the settings tab; your tenant administrator may also set team defaults, which a repository's
  declaration and your own template override. A pull request launches on its own head branch instead (below).
  An agent asks the same rules through the af tool `branch_name`, so a skill that creates
  branches gets the name the dialog would show instead of inventing its own.
- Once work is under way, **Comment the work back** appears in the details. The draft holds the
  branch and the changed files, and **the sentence is yours**: it is posted exactly as written and
  nothing is written for you. On a Bitbucket pull request the button appears only when the
  connection is known to hold the pull request write scope (`pullrequest:write`; see
  [15](15-external-services.md)); otherwise Agent Fleet only reads from Bitbucket.
- A session is linked to more than the ticket it was started from. A session started by another
  session watches its parent's tickets; an agent that names its branch after a ticket with
  `branch_name` is working on it. The pull request of a session's branch is linked to it, and so
  are the Jira keys in the branch name or the pull request's title (`PROJ-123`, upper case, only
  for projects of your Jira connection) and the GitHub issues the pull request closes; these stay
  linked after the session moves to another branch. An agent can also link itself to a ticket or
  pull request with the af tool `link_work_item`. The linked
  items appear on the session's row (click one to open its details), and the details list every
  session linked to the item with how it is linked. While the workspace is stopped, rows and
  details show the links as they were when it last ran. A linked pull request last seen merged or
  closed is drawn faint at the end of the row and tagged with its state in the details. To unlink
  a session, press **Remove** next to it in the item's details; a session's launch item cannot be
  removed, and an item removed this way is not linked again from the session's branch or pull
  request.

While the workspace runs the list refreshes about every 5 minutes; **⟳** fetches immediately.

### Pull request details

A **pull request row on GitHub or Bitbucket is read again the moment you open it**. The panel then
shows what the cached list cannot carry: **draft or not, conflicts, where the reviews stand, whether
CI is green, base ← head and the size of the diff**. Every one of those can change between two
five-minute refreshes, and a stale copy of them is worse than none.

- The first line always says which of the two you are looking at: the live pull request, or the
  last fetched copy. While the workspace is stopped there is nothing to read it with (**opening a
  panel never starts your workspace**), so the fetched copy stays on screen; if the read fails, a
  "Try again" appears.
- The main button is **Open in GitHub / Bitbucket**. The diff and the review thread live there, not
  in Agent Fleet.
- **Merge (GitHub only).** A GitHub pull request that GitHub says can be merged has a **Merge** button
  next to the main button. It opens a confirmation that names the pull request, **base ← head** and the
  merge method (only the methods the repository allows; the first of merge / squash / rebase is
  preselected), and nothing is merged until you confirm. Only the commit the panel displayed is merged: if
  the branch has moved since, GitHub refuses and the panel reads the pull request again. The button is
  disabled, with the reason beside it, while GitHub does not accept a merge (blocked by required reviews or
  checks, behind its base, conflicts, failing checks, a draft, or not decided yet), and while the
  workspace is stopped (the GitHub credentials live in it; it is never started for this). It is not drawn
  at all unless the connection is known to be able to merge: a classic token (what the GitHub device-flow
  login gives) with the `repo` scope (`public_repo` is enough on a public repository). A GitHub App connection gets the button only when GitHub positively shows
  that its installation grants Contents: write on this repository and you have push access there; if that
  cannot be read (an error, a rate limit, a timeout) it gets none. A fine-grained token does not report what it may do,
  so it gets no Merge button; merge on GitHub. **Bitbucket pull requests have no Merge
  button**, and neither does any agent: merging is something you press. The branch is not deleted
  afterwards.
- **Starting a session** is one fold away, and here **Start is a review button**: a new worktree
  checks out **the pull request's own head branch** rather than cutting a fresh one, and the first
  instruction asks for a review, not a plan. When a working copy on that branch already exists,
  **where to work** defaults to it; a copy you pick by hand keeps whatever branch it is on. The
  started badge keeps working for it.
- **The description is still not shown here.**

### From a ticket number in the conversation

In the mirror and the assistant chat, **ticket references the agent writes become links** that open
the same details panel, even with the left pane collapsed. Ctrl/⌘-click or a middle click goes
straight to the tracker instead.

- A `#956` in running text links, merged and closed ones included. In a range such as `#573-#598`,
  both ends link.
- **A number in backticks, such as `` `#166` ``,** links only when your Issue tracker list holds it,
  so a number quoted as something you typed does not turn into a link to an unrelated issue.
- `#956` is read as a number in the session's own repository (its origin on github.com or
  bitbucket.org). The chat has no working copy, so there only the `owner/name#956` form links.
- A Jira key (`PROJ-123`) links when the list holds an issue of that project, or when the project
  is one your Jira connection can browse. In backticks it links only when the list holds that
  issue. Look-alikes such as `UTF-8` or `SHA-256` stay text unless a project has that key. In a Bitbucket
  working copy, `#N` links only the pull requests the list holds.
- A ticket that is not in the list opens with its key and a link to the original page; a session can
  still be started from it. While the workspace runs, a GitHub, Bitbucket or Jira ticket is read
  live when the panel opens. A stopped workspace is not started for it: the panel says **"The
  workspace is stopped, so … was not asked"** and keeps the key and the link.
- How links in files you open (rather than in the conversation) behave is in
  [07 Files](07-files.md#links-in-rendered-markdown).
- Someone reading a shared session sees these as plain text.

## Reading state — badges and notifications

In each row of the list, the colored icon at the front shows the agent kind, and the state icon
at the end shows the current state. Hover over the state icon to see the state name. States that
need action from you (**Question**, **Plan ready**, **Awaiting permission**) also show text.
Active sessions refresh automatically every 4 seconds.

The states and what each one means (Working…, Question, Plan ready, Awaiting permission, Ready,
Stopped, a usage or spend limit, and so on) are listed in one place:
[Icons, badges, and menus](badges-and-menus.md#session-display).

The other marks on a row (colored pane numbers, reading aloud, branch-switch warnings, the unread
red dot) and the right-click menus are in the same chapter.

When a state changes while you're not watching, a **browser notification** appears (suppressed
while you have that screen open). When the work pauses (the session becomes Ready) you're
notified with **"A reply is ready"**; when a question arrives, with **"A question is waiting"**.
The session name is included in the body. This suits use cases like waiting for a reply on your
phone during a commute (shell / ssm don't notify). Which kinds of notification pop up, speak, or
leave an unread dot is chosen per kind in [Settings › Notifications](20-settings.md#notifications).

When a program running in a session **asks its terminal for a notification** (the OSC 9 / 99 /
777 escape sequences; you can emit one yourself, e.g. `printf '\e]9;Build done\a'`), it
arrives in the notification center as **"Terminal notification"**, with a browser notification
too (not while that session is in the active pane, nor when the browser has not been allowed to notify).
That lets a shell session tell you when a long build finishes. claude / codex / opencode already
report the same moments through their own channel, so their terminal notifications are not used,
to avoid duplicates. The exception is claude's own notification tool (`PushNotification`, off by
default in claude): when claude decides to notify you, that message arrives as a terminal
notification too, unless claude held it back because you were active in that terminal.

A session with a notification you have not read gets a **red dot**; what sets it and what clears it
is described under [Icons, badges, and menus](badges-and-menus.md#session-display).

## Changing the title and branch name

Both are in the session's ⋯ menu (or right-click). The same menu also has **"Menu of repository
{name}"**, which opens the menu of the session's repository in the left pane, so commit, switch
branch and the like are one step away from a session you are looking at. The conditions for showing it are under
[Icons, badges, and menus](badges-and-menus.md#sessions).

- **Rename**: changes the identifying name in the list. Saving it empty reverts to the automatic name (repository name + timestamp). **"Ask AI to suggest"** has a name proposed from the conversation contents. A session started from an issue keeps the issue's key (`#1146`, `PROJ-123`) at the front; adopt it with "Use this".
- **Rename the branch**: appears only for sessions running in a worktree. Renames that worktree's branch (the folder, that is the session, stays as is). Buttons let you swap the prefix for one of the repository's branch naming rules (`feature/` `fix/` `hotfix/` … by default), and **"Ask AI to suggest"** proposes a branch name from the conversation, composed by the same rules. A session started from an issue keeps the issue's number in it. A name outside the rules only gets a note. Use it to give a meaningful name later to a session started under a provisional name (`temp/…`).
