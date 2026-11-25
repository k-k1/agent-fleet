# 0145. An Active view in the left pane: the sessions that are going on now

English | [日本語](0145-active-sessions-view.ja.md)

- Status: **accepted** (2026-11-25). Scope, the 24-hour window and the folded default chosen with the owner. Decisions 1 to 7 are implemented in #5156, which lands after this record; decision 8 waits on the wire field.
- Tracking: #5154 (the view). Follow-ups: #5155 (recently stopped sessions).
- Related: [0140](0140-console-activity-bar.md) (the activity bar this adds a view to) /
  [0078](0078-sessions-overview-pane.md) (the overview pane whose grouping and card this reuses, and whose rejected alternative this revisits) /
  [0142](0142-spaces-start-hub-terminal-view.md) decisions 9 and 11 (the tree's three sections; an icon can be turned off) /
  [0096](0096-fleet-session-graph.md) (one fold rule for every view)

## Context

The Sessions view is an inventory by place: repository, then working copy and branch, then the
session. A member with many repositories and worktrees reads past rows of branches and stopped
sessions to find the few that are running, three levels down. The view organised by activity is
the sessions overview (0078), and it is a pane: looking at it costs a pane of the main area.

0078 rejected "widen the left pane into cards" because the pane was then a stack of eight
sections and the cards would have pushed the rest out. 0140 has since made the pane one
full-height view at a time, and a list that wants the height gets a view of its own.

The Console cannot tell when a session stopped. The Agent keeps the time in the session's meta
(`Meta.StoppedAt`), but the session DTO does not carry it, so neither the Control Plane's relay
nor the Console has it.

## Decision

1. **Active is a view of the activity bar** (`RAIL_VIEWS` gains `active`, icon `pulse` with the
   agent mark, right after Sessions). The Sessions view does not change and stays the default.
   The icon can be turned off like any other.
2. **It lists what is going on, not what exists.** Running sessions; stopped sessions that still
   need the member (stopped on an unanswered exchange, or holding a handoff nobody took); and
   every descendant of a listed session, so a family is never drawn with holes. Shells, SSM
   logins, unattended runs and shared sessions stay in their own views; the worktree sessions of
   a schedule set to show in the Sessions view are listed here as they are there. The working
   set narrows the view as it narrows the Sessions tree: a session goes by its working copy.
   A corrupted lineage cycle is opened at one member, so a family always has a card to open.
3. **Groups are the tree's three sections in the tree's order:** repositories, spaces, other. A
   repository is identified by its remote, as in the overview, so the base clone and every
   worktree share one heading and the branch level is gone. A group exists only while it holds a
   listed session. Headings sort by name inside each kind and do not move when a session starts
   waiting.
4. **Families fold, and start folded.** Inside a group families are ordered and folded by the
   overview's functions (`orderByFamily`, `foldFamilies`): the family that holds a waiting
   session comes first. What is stored is the parents the member opened, per browser, member and tenant, so a parent
   that gains its first child starts folded without anything having been recorded. A folded
   parent says how many rows it hides and, separately, how many of them wait for an answer.
   > **Note (2026-11-25, #5165).** The group headings fold as well. What is stored is the groups the
   > member folded, per browser, member and tenant (the rule of every other view, 0096), so a group
   > that appears for the first time starts open. A folded heading keeps its counts: running / total,
   > and how many of its sessions wait for an answer. The heading is a disclosure button.
5. **The card is the overview's card in a rail form,** not a second component: one column at the
   pane's width, without the repository line (the heading says it), the spend trend and the start
   time. It carries the pane numbers, which the grid's card leaves out, because in the left pane
   they are how a session is found among the panes (0140 decision 7). A click opens the session
   in the focused pane, as a row of the Sessions view does.
6. **A waiting session is counted on one icon.** The two views list the same waiting sessions.
   While the Active icon is in the bar it carries the count; when it is turned off the Sessions
   icon does, as before; so does it while the workspace is not running, when Active lists
   nothing. Only running sessions are counted. The unread dot stays on Sessions, the only view that lists every
   session a notice can be about.
7. **Reveals are unchanged.** The existing reveal signals name a working copy, not a session, and
   a working copy's row is in the tree.
8. **A stopped session stays for 24 hours** after it stopped (fixed, not a setting), and the rows
   that left the window are counted in one line at the foot of their group. This needs
   `stoppedAt` on the session DTO, the Control Plane's relay and the Console type (#5155). For a
   stopped workspace the view is then drawn from the Control Plane's mirror; until then it shows
   the placeholder the other views show.

## Why 24 hours

A workspace stops overnight. With a one-hour window the view is empty every morning and the
member goes back to the tree. With 24 hours it is where yesterday's work is picked up.

## Alternatives not taken

- **A switch inside the Sessions view (by place / active).** No second icon and no badge to
  route, but the form used most sits one click behind the other, and an icon is what a member
  can turn off.
- **A "running only" filter on the tree.** The cheapest. The three levels stay, and a row cannot
  say what its session is doing.
- **Moving the overview pane into the left pane.** The pane is what is watched on a wide screen
  or a second window; the view is what is steered from while working. They share the functions
  and the card.
- **Leaving the session rows out of the Sessions view** now that Active lists them. 0142 keeps a
  row out of a second view (a shell is listed once), and that holds between two inventories.
  These two answer different questions, and the tree without its sessions could no longer say
  what runs in a working copy. The withdrawn "put stopped sessions away" proposal is not
  reopened: the tree keeps every row.
- **Floating a waiting group to the top.** A list that is watched must not reorder its sections;
  the badge and the jump to the next session that needs you already do that job.
- **Opening a folded family by itself when a child starts waiting.** The list would jump under
  the pointer. The count on the parent says it, and the family moves to the head of its group.

## Consequences

- The bar has one more icon, and two of them are about sessions. The labels have to keep them
  apart: Sessions is where things are, Active is what is going on.
- `overview.css` now holds the rail form of the card as well. A second file redefining the same
  classes would be a second place to keep in step, and the duplicate-class check forbids it.
- A long-lived parent accumulates stopped children; they are listed under it (folded) until the
  member deletes them, as in the overview.
