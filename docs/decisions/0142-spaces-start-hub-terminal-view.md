# 0142. Spaces: a place to run agents without a repository, the Start hub, and a Terminal view

English | [日本語](0142-spaces-start-hub-terminal-view.ja.md)

- Status: **accepted** (2026-11-24). Decided with the owner on a static mock. Nothing is built.
  Open questions 3 and 4 were decided the same day; 1 and 2 are not decided.
- Tracking: #4961. Follow-ups: #4963, #4964, #4965, #4966, #4967, #4968.
- Related: [0140](0140-console-activity-bar.md) (the activity bar this adds a view to; decisions 2,
  4 and 5) / [0081](0081-image-generation-pane.md) decision 1 (no engine, no button) /
  [0104](0104-long-lived-member-workspace.md) (one long-lived home per member) /
  [0073](0073-session-spawned-sessions.md) decision 7 (two agents in one working copy) /
  [0108](0108-af-owned-agent-memory.md) (memory projects) / [0103](0103-branch-naming-rules.md)
  (worktree branch names)

## Context

Agent Fleet assumes that work happens in a working copy under `~/repos`. Some members do not use
repositories: they draft documents, summarise spreadsheets and research. They have two places to
run an agent today, and both are wrong for them.

- **"Launch an agent in home"** runs the agent in `~`. Its output lands loose in the home
  directory, mixed with dotfiles and other sessions' files. One measured workspace had 193 loose
  items in `~` ([log/116](../log/116-home-clutter-and-cache-growth.md)). A home launch offers no
  skills, and only the member's own templates.
- **`~/.af-work/<session>`** (`paths.SessionWorkDir`) is per session, but it is the agent's scratch
  area. It is removed when the session is deleted, and a week after the session is archived.
- **A new folder in `~/repos`** ("Start in a new folder…") puts the work where **Recreate deletes
  it** ([guide: recreate](../../guide/user/workspace/recreate.md)). A member without git has
  nothing pushed to fall back on.

Five facts in the code shape the answer:

1. The server resolves `dir: ""` to the home directory (`sessionx/session_create.go`,
   `resolveWorkingCopy`). Nothing else marks a home session.
2. A session's id derives from its directory and its slug (`session.UUID(dir, slug)`), and Claude
   stores the transcript under a path derived from the working directory. **Moving or renaming a
   session's folder breaks `--resume`.**
3. "Other sessions" (`pj.other_sessions`) is a catch-all. It lists every session whose folder name
   (`sessionFolder`, the basename of `dir`) matches no working copy: home shells, home agents, and
   sessions whose repository was removed. A folder name alone cannot tell a space `foo` from a
   repository `foo`.
4. Agent memory maps only directories under `~/repos` to a project
   (`memoryx.agentMemProjectFor`). A session outside it has no memory project.
5. Deleting a working copy also deletes the shell and SSM sessions opened in it (they go to the
   trash). Shells are tied to the place they were opened in.

## Decision

1. **Spaces live in `~/spaces/<folder>`.** Not in `~/repos` (Recreate deletes it), not in
   `~/.af-work` (removed with its session), and not loose in `~`. Recreate keeps `~/spaces`; Clean
   deletes it, like the rest of the home directory.
2. **The unit is the space, not the session.** The Start hub creates a new space by default, and
   an existing space can be picked again, so work that spans days and several sessions stays in one
   folder. Deleting a session keeps its space. Deleting a space deletes its sessions too, through
   the trash, the way deleting a working copy does today.
3. **A space folder is never renamed or moved** (fact 2). Its display name is stored apart from the
   folder name and can change. The folder name is assigned at creation and is unique.
4. **There is no "simple mode".** Developers use spaces too: as a desk for research and documents
   that do not belong in a repository. A per-member switch would hide spaces from them or
   repositories from the others. The Console shows what exists and what can be used, as it already
   does (ADR 0081 decision 1, ADR 0140), and the member can turn views off (decision 11).
5. **The Start hub ("Start") is an accordion** with exactly one section open:
   - *Start in a space*: the prompt ("What would you like done?"), attachments, the place chip, and
     the recently used spaces;
   - *Start in a repository*: the current repository list;
   - *Other*: "Launch an agent in home", shell, "SSM — log in to another host", "Clone a new
     repository…" and "Start in a new folder…", the rows the hub has today.

   The hub remembers the last opened section, per user and tenant like the rail's fold state. Every
   way into the hub (the WS bar's Start, Alt+N, Home, sending a memo) opens that section. Space comes
   first the very first time. **The prompt and its attachments are one draft for the whole hub**,
   shown in every section, so switching sections keeps the text and moves the focus to the open
   section's input. A repository row carries the draft into the repository launch dialog. In
   *Other* the draft is used only by the home launch, and the section says so.
6. **The "Chat (assistant)" row leaves the hub.** Assistants keep their own view, and conversations
   still start from it, from Home and from the onboarding card.
7. **Launch settings come in layers.**
   - Layer 0: what to do (the prompt and attachments).
   - Layer 1, the place: the place chip offers spaces only (new, recent, all). A repository is picked
     from its section.
   - Layer 2, "Details": agent, model, effort, driver and permissions. **No branch.** A branch or a
     worktree is a choice of place, so it stays in the repository launch dialog.

   A space launch starts directly from the hub, without a second dialog. The repository launch
   dialog (`LaunchModal`) stays for the repository section, the repository rows, source control,
   work items and handoffs, reordered as *what → where (working copy, base, branch, subdirectory) →
   details*. The home variant ("Start working: Home") stays for *Other → Launch an agent in home*.
8. **Driver defaults.** A harness with a managed driver launches managed in a space. Claude has only
   the TUI, so it launches in the TUI, and its pane opens in the chat view (`pane.content.chat`,
   the existing chat / terminal toggle). The terminal stays one click away.
9. **The Sessions view has three sections:** Repositories, Spaces and Other. Spaces and Other are
   hidden while empty, as Other is today. The repositories empty state ("No repositories") stays.
   Spaces and repositories are told apart by the session's directory root, not by its folder name
   (fact 3).
10. **A Terminal view lists every shell and SSM session** (a list, so a view: ADR 0140 decision 2).
    - Shells are grouped by the place they were opened in: home, a repository, a space. The shells
      the database tab opens (`af-db connect`) group under home.
    - SSM sessions are grouped by machine: account, region and instance id. A group is labelled with
      the registered host's alias, or the instance id when no host matches. A different profile or
      run-as document shows on the row, not as a separate group.
    - The view's header starts a shell (home, a repository or a space) or an SSM session.
    - A repository or space row in the Sessions view shows only a count chip. The chip switches to
      the Terminal view and reveals that place. "Open shell here" stays on the tree's context menu.
    - Opening a shell does not switch the left pane (ADR 0140 decision 5).
    - The icon is on the bar from the start. *Other → shell* stays in the hub.

    The Sessions view stops listing shell and SSM sessions, and the "tidy" action for shell and SSM
    sessions moves to this view.
11. **The activity bar gets a context menu** (right-click; long-press on a phone; the context-menu
    key) that turns each view's icon on or off. The view on screen and the last icon cannot be
    turned off. A view that cannot be used (Schedules without a scheduler) is listed greyed out with
    the reason. "Reset" restores the defaults. The choice is stored with the member's UI preferences
    on the Agent, so it follows them across browsers.

## Phases

- **P1**: the Agent creates and lists spaces (`~/spaces`, a marker file holding the display name);
  the hub becomes the accordion with the shared draft; a space launch starts directly; the Sessions
  view gets its Spaces section.
- **P2**: recently used spaces and "pick an existing space"; attachments copied into the space;
  deleting a space through the trash; a home-usage category for `~/spaces`; the Terminal view,
  including the wire change SSM grouping needs.
- **P3**: the activity-bar context menu; the decisions on open questions 1 and 2; the guide pages.

## Open questions

1. **Default permissions in a space.** (a) the agent's usual default; (b) auto-approve edits inside
   the space and ask for everything else; (c) a tenant default chosen by an administrator.
   Recommended: (b) with (c) able to override it, provided every Claude approval reaches the chat
   view. That still has to be checked on a live session.
2. **Memory scope for spaces.** (a) no memory, as today; (b) one shared project for all of a
   member's spaces; (c) one project per space. Recommended: (b), because spaces are often
   short-lived and (c) would scatter memories over many small projects.
3. **The badge of a view whose icon is off.** ADR 0140 decision 4 assumes every view has an icon.
   (a) drop the badge; (b) show a dot on the bar and name the view in the context menu.
   **Decided: (b) for waiting badges** (a session stopped on the member's answer), **(a) for news.**
4. **The wire change for SSM grouping.** The stored meta has the SSM target (`session.SSMMeta`:
   instance id, region, account id, profile), but the session list does not carry it. (a) add those
   non-secret fields to the list; (b) group from the title. **Decided: (a).** The title can be
   renamed, so (b) is not a grouping key.

## Alternatives set aside

- **Spaces in `~/repos`.** Recreate deletes it, and members without git have nothing pushed.
- **Using `~/.af-work/<session>` as the working directory.** It is the agent's scratch area and is
  removed with the session. The two stay separate: `~/.af-work` for the agent's throwaway files,
  `~/spaces` for the member's work.
- **Keeping the home launch as the only answer.** It is what produced the clutter.
- **One folder per session, fixed.** Work that spans sessions would scatter over many folders, and
  the folder's life would follow the session's retention.
- **A "simple mode" chosen per member** (by the member, by an administrator, or detected from the
  absence of repositories). It hides spaces from developers, puts a cliff in front of a member who
  starts using git, cannot be discovered from inside, and doubles the guide and the tests.
- **Hub sections that fold independently.** Every combination of open sections had to be designed,
  and a draft could sit inside a folded section. The accordion has one remembered value.
- **Repositories in the place chip.** They would appear in two places in the hub.
- **Listing shells in both the Sessions view and the Terminal view.** Each shell would be listed
  twice. The count chip keeps the link to the working copy visible.
- **No TUI at all for spaces.** Claude has no managed driver, so spaces would lose Claude.
- **A separate group for database shells.** Nothing marks a session as opened from the database
  tab, and there are few of them. A label on the row is enough until that changes.

## Consequences

- `~/spaces` is new data in the home directory. The guide's recreate table, Settings → Machine's
  home usage and the cleanup categories need a row for it.
- Sessions in spaces have no memory, skills from a working copy or repository templates until the
  open questions are decided.
- "Other sessions" shrinks to home agents and sessions whose repository was removed.
- Moving an existing session into a space, and promoting a space into a repository (copy the files
  into a new repository and start a new session there; a move would break `--resume`), are not part
  of this record.

## Amendment: how deleting a space works (#4965 part 3)

Decision 2 says a deleted space goes through the trash with its sessions. As built:

- **The folder is moved, not archived.** The trash entry is the usual archive (a manifest, no
  payload) and the folder waits beside it as `<id>.space`. A rename keeps every file, mode and
  time whatever the size; a tarball would be held in memory and capped at the per-entry limit a
  restore reads. It needs `~/spaces` and the trash on one file system, which is checked before
  anything is changed (a workspace's home is one; if not, nothing is deleted). Purging the entry
  removes the folder.
- **The sessions follow a working copy's delete (ADR 0101 decision 4).** A running or locked
  session refuses the delete. Stopped shell / ssm sessions go to the trash first (a failure stops
  the delete), then the folder moves, then stopped AI sessions go to the shelf. Restoring the entry
  moves the folder back under the same name (so session ids and Claude's transcript paths hold)
  and takes the shelved sessions off the shelf; it is refused while anything is at that name.
- **A delete blocks a session start in that space** (the "being deleted" mark a working-copy delete
  uses), and waits for a rename or an attachment copy in flight (a read-write lock in `spacex`).

Settling the open points of #5085 that this touches:

- *A paste to a session whose space is gone* (item 2). Lifetimes are tied through the delete: a
  space deleted from the Console takes its sessions off the list, so they are not pasted to. A
  space removed some other way (Clean, a folder removed by hand) leaves the paste an ordinary upload:
  it answers 201 with the stored path and no `spaceCopy`, and does not recreate the folder.
  A test pins it. Storing the space on the session at creation is not needed.
- *The space copy is confined by `os.Root`, not per-component `O_NOFOLLOW`* (item 3). The delete
  takes the same lock as the copy and the rename, which closes the overlap this Agent can cause
  itself. The two compositions that need another process of the same user to rewrite the workspace
  during the upload stay open, as before: that process already has the user's rights.
- *Archived sessions and the recent order* (item 1) is unchanged: a deleted space is gone from the
  list, so it has no rank to lose.
