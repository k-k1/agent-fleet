# 0140. The Console's left pane: an activity bar and one view at a time

English | [日本語](0140-console-activity-bar.ja.md)

- Status: **accepted** (2026-11-16). Decided with the owner on a static mock, then built.
- Tracking: #4350.
- Related: [0011](0011-console-rebuild.md) (the shell this changes) /
  [0036](0036-working-sets.md) (the working-set switcher, which keeps its place) /
  [0078](0078-sessions-overview-pane.md) (the overview is a pane, so its entry point is a launcher) /
  [0081](0081-image-generation-pane.md) decision 1 (no image engine, no button)

## Context

The left pane stacked eight collapsible sections in one scroll: assistant, schedules, work items,
memo queue, repositories, other sessions, shared sessions, files. Three things followed.

- **The session list was pushed off screen.** It is what the pane is mostly used for, and a work
  item list of forty rows sat above it. Collapsing sections by hand is the member doing the
  layout's job.
- **The WS bar kept growing.** It held seven labelled buttons. Each new pane kind (sessions
  overview, image studio, Home) added one, because the other entry point, the layout map at the top
  of the pane, hides itself while there is a single pane. The chips on the bar's right needed a
  width-driven fold to survive.
- **Nothing told two kinds of control apart**: the ones that change what the left pane shows, and
  the ones that open something in the main area.

## Decision

1. **An icon column (the activity bar) runs down the left edge.** The top bar's menu button, the WS
   bar's power button and the column share one width, so they read as a single column.
2. **An icon does one of two things, and its position says which.**
   - *Views* (the upper group) choose what the left pane shows: Sessions (repositories, other
     sessions, shared sessions), Assistant, Work items, Memo queue, Schedules, Files.
   - *Launchers* open a pane and leave the left pane alone: Home directly under the power button,
     and the sessions overview, image generation and generated images at the foot.
   A new pane kind gets a launcher. A new list gets a view. Neither goes on the WS bar.
3. **The left pane shows one view at a time.** The icon of the view on screen folds the pane away;
   the column stays, so every view is one click from anywhere. Every view stays mounted and only
   the chosen one is displayed, because a section's poller, its draft and its scroll position live
   in the section. The exception is a view that reads the Workspace's file system (Files): it
   mounts on its first show, and while it is not on screen it makes no read of its own; a turn
   that ended meanwhile is read once when it is shown again (#4426). A hidden view that read
   like a shown one sent ~1,360 listings on every Console load and stalled the terminal's
   WebSocket.
4. **A badge is a notice, not an inventory.** It marks something that arrived in a view that is
   not on screen. There are two kinds, and they clear differently:
   - *Waiting* (Sessions, an amber count): running sessions stopped on the member's answer. It
     stays until the answer is given, also while the view is open: looking at the row does not
     unblock the session.
   - *News*: unread sessions and unread assistant conversations (a dot, cleared by the existing
     notification rules), work items that moved (a count) and schedules whose latest run failed (a
     red dot). For the last two, opening the view acknowledges what is unread at that moment; the
     rows keep their own marks.
   A view whose rows only the member adds (the memo queue, files) has no badge. A badge counts
   only rows its view lists: with a working set active, sessions, conversations and schedules
   outside the set do not count, and a failed run the view does not list is not acknowledged by
   opening it. A count with no row behind it in the view the badge leads to is worse than no
   count; the working-set bar, shown on every view the set narrows (sessions, assistants,
   schedules, files), is what says the rest is out of sight.
5. **A reveal brings its view forward.** Features keep firing the reveal signals they had; one
   module maps each signal to its view. A reveal that is a side effect of something finishing (a
   clone landing) does not switch the view.
6. **The WS bar holds the workspace, "Start" and the pane buttons.** "Start" is the only labelled
   button; split right, split down and close all are one icon-only group. The "Workspace" caption
   is gone: the state text beside the power button says what it powers.
7. **The layout map is removed.** Row ordinal badges and the panes' corner chips already say which
   session is in which pane, and its buttons are now launchers.
8. **On a phone the column and the pane slide out together as the drawer.** An icon there only
   selects; there is no folded pane inside a drawer.

## Alternatives set aside

- **Removing the top bar** and moving the account, notifications and inbox to the foot of the
  column, as VS Code does. It frees a row of height, and it was the first proposal. The owner chose
  to keep both bars; the foot of the column went to the launchers instead.
- **Stacking two views** (sessions above the memo queue, say). It keeps part of the old at-a-glance
  reading, at the price of a second fold state per view and a phone layout for it. The badges are
  the cheaper answer to "what happened where I am not looking", and the session list keeps the full
  height, which is the point of the change.
- **Keeping the stack and adding the column as a table of contents.** The smallest change, and it
  leaves the session list where it was.
- **Unmounting the views that are not shown.** It would save their polling, and it would drop memo
  drafts and scroll positions on every switch and leave the badges without data.
- **A hover hot zone that peeks the folded pane.** The column now occupies that edge, and a pointer
  on its way to an icon would trigger it.

## Consequences

- The pane is 320px beside a 48px column: 28px more than the 340px pane it replaces.
- The stored fold of a section that is now a whole view no longer applies; the section is always
  open there. The keys are left in place.
- The acknowledged news is kept per browser (`af-rail-acked`), like the work-item baselines it
  builds on. Losing it shows one badge again, never a wrong one.

## Note, 2026-11-19: vertical swipe on a phone (#4557)

On a phone with the drawer out, a vertical swipe that starts on the column steps the left pane to
the previous / next entry of the visible views (swipe up = next; one constant, `BAR_SWIPE_UP_IS_NEXT`
in `console/src/app/barSwipe.ts`). It clamps at the ends, takes one step per gesture, goes through
`showView` (so it persists and nothing unmounts), and is live only while the column fits: an
overflowing column keeps native scrolling. It lives in its own hook (`useBarSwipe`), not in
`installSwipeGestures`, whose horizontal gestures are unchanged. Home and the foot launchers are never
targets. Tablet and desktop keep taps only. Direction and clamp-vs-wrap still need a check on a phone.

## Note, 2026-11-20: memos waiting for approval are a waiting badge (#4607)

The first sentence of `badges.ts` said the memo queue has no badge because only the member adds its
rows. Sessions and `/mcp` connections now add memos that wait for the member's approval (state
`held`), and nothing runs until they are approved. That is the same case as a session waiting for an
answer, so the memo icon carries a `waiting` badge counting them: it stays while the view is open
and clears as memos are approved or removed. Decision 2 above is unchanged. The memo list is read
for the count by the memo panel, which App mounts for the whole session and which
publishes each read it makes; a small feed reads on its own only when no panel is mounted. Reads are
numbered when they start (a slow older answer cannot overwrite a newer one) and tied to the scope
they started in: the tenant while the workspace runs, none while it is stopped, so a stopped
workspace or another tenant never shows a count. A working set does not narrow it, because the memo view does
not either.

## Note, 2026-11-21: shared sessions get their own view (#4645)

Shared sessions sat at the bottom of the Sessions view, far below the fold under a long repository
tree. They are now a view of their own, `shared` (icon `broadcast`), right after Sessions, in the
running and the stopped workspace alike; the Sessions view no longer lists them. Decisions above are
unchanged. Decision 4: the icon carries a `waiting` badge counting handoffs offered to the member
that wait for their decision, read from the handoff store the view's section already polls. There
is no news dot, because nothing client-side marks a shared session unread (notifications never
target the shared list). Decision 5: opening a handoff-offer notice brings the view forward. The
phone's vertical swipe follows the icon order, so it includes the new view. The view is not scoped
by a working set: a shared session belongs to another member's working copy. An empty view says
that nothing is shared instead of staying blank.

## Note, 2026-11-21: memo auto-run gets its own view; unattended work leaves the tree (#4646, part 1)

Memo auto-run runs sat in a folded "Auto-run" group under each repository (#4627), which is still a
long tree with a branch header per worktree. They now have a view of their own, `autorun` (icon
`play`, label "Auto-run"), right after Memo queue, in the running and the stopped workspace alike. It
lists one row per run (a session with origin `queue`), not one per worktree, under its queue's
category: state (running, waiting for the member's answer, ended), the work item from the session's
`started` link, the pull request when known, and the time it has been going. A row opens the run's
session; the sessions the run started (origin `session` edges up to the run) sit one level under it;
ended runs fold into "Ended N". The category of a run comes from the queue's deliveries, which the
view reads while it is mounted; a run no delivery names is listed under "Other runs". The view is not
scoped by a working set. Decisions above are unchanged.

The repository tree no longer shows auto-run copies or sessions. They are carried in the owner's
`detached` list, so bulk stop and delete on a base clone still count and name them (the confirmation
labels them "Listed elsewhere"). Scheduled sessions keep their tree group in this part: the view that
is to list them (the Schedules view) is a later part, and removing the group first would leave them
nowhere. Later parts: the row actions and "Clear finished runs", the badges (decision 4: a waiting
count, a failed dot, only for rows the view lists), scheduled sessions in the Schedules view, and the
memo run history linking to a row. Follow-ups: #4646.

## Note, 2026-11-21: the Auto-run view gets a section head, a working-set scope and repository-style categories (#4670)

The view now has a section head like Shared sessions: the Auto-run icon, the title, the number of
running or waiting runs, and the actions (Clear finished runs, refresh). It is scoped by the working
set, which supersedes "not scoped" in the note above: a category is listed when its repository is in
the set, and a category without a repository, like the runs no category names, under every set. A
category is a sticky header like a repository row (repository icon, `repo / category`, a gear for its
auto-run settings, a menu with the run history and "Show in Memo queue"), with two sticky sub-headers
under it: "Running / waiting N" (open) and "Ended N" (folded). A run whose delivery is closed but whose
root session is alive (taken over, or started again) is listed under Running with a "resumed" tag
instead of Ended. Decisions above are unchanged.

## Note, 2026-11-21: badges for the Auto-run icon; scheduled sessions move to the Schedules view (#4646, part 3)

The Auto-run icon takes decision 4's two badges, counted only over the rows the view lists (every
run of the active working set's repositories, #4670's scoping, which the count and the
acknowledgement share with the view). A waiting count: the runs whose session waits for the
member's answer, which stays while the view is open. A red news dot: a run its queue closed after
its first turn failed (`firstTurn.outcome` is `turn-failed`; a delivery has no failed outcome of its own,
and an aborted or hand-ended run is the member's own doing), acknowledged by opening the view like the
Schedules dot. Acknowledged keys are never pruned by a read that lacks the run. Both need the runs while the view is off screen, so the queue list and its
deliveries are read by a shared feed that runs while the workspace runs or the view is shown, and
the view reads from it. The delivery read also asks for every open run (`?include=open`), so a run
older than the newest fifty keeps its category.

Scheduled sessions (origin `schedule`) leave the repository tree and the Sessions list too, so the
tree's "Schedules" group is gone and its copies are carried in `detached` like auto-run's. The
Schedules view lists them below the schedules: one row per scheduled run, the sessions it spawned one
level under, finished runs folded. A session carries no schedule id, so they are listed as one group
rather than under each schedule; the Schedules icon gets a waiting count for them (they no longer
count in the Sessions badge). The memo run history links to a run's row in the Auto-run view.
Follow-ups: #4646.

Note (2026-11-24, #5070): each schedule gets a setting, "Also show in the Sessions view", off by
default. When it is on, the sessions that a run of that schedule started and that have their own
worktree are treated as the member's own: they are listed in the repository tree under their working
copy and in the Sessions list, their worktree is an ordinary tree row, the filter finds them, and they
count in the Sessions badge instead of the Schedules one. The run's root session and children without
a worktree stay in the Schedules view only, and every run keeps its place there. A root without a
schedule id, or whose schedule is gone, keeps the rule above. This amends the rule above for those
sessions only; the decision itself stands.
