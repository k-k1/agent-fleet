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
   in the section.
4. **A badge is a notice, not an inventory.** It marks something that arrived in a view that is
   not on screen. There are two kinds, and they clear differently:
   - *Waiting* (Sessions, an amber count): running sessions stopped on the member's answer. It
     stays until the answer is given, also while the view is open: looking at the row does not
     unblock the session.
   - *News*: unread sessions and unread assistant conversations (a dot, cleared by the existing
     notification rules), work items that moved (a count) and schedules whose latest run failed (a
     red dot). For the last two, opening the view acknowledges what is unread at that moment; the
     rows keep their own marks.
   A view whose rows only the member adds (the memo queue, files) has no badge.
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
