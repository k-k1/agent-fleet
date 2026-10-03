// Rotation across running sessions — the selection rule behind the phone's swipe gesture
// (app/App.tsx).
//
// This module is PURE (no store, no DOM) so it can be unit-tested in the node vitest
// project; the side-effecting part that actually opens a pane lives in sessions/open.ts
// (the same split as workingSets.ts vs workingSetsStore.ts).
//
// Candidates and order (respecting the working sets of docs/log/52):
// - Only alive agent sessions. A stopped one is not a switch target; it needs a decision to
//   resume. A shell / ssm session is a terminal, not a conversation to skim through.
// - When a working set is selected, follow that filter, so the set matches what the left rail
//   shows.
// - The order is the left rail's, top to bottom (railOrder), so a swipe lands on the row
//   next to the current one. The raw GET /api/sessions order (newest first across every
//   folder) stopped matching the rail once worktrees nested by lineage.
import { sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import { orphanSessions, repoTree, sessionsInFolder } from "../../lib/project.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

/** Every session in the order the left rail lists it: the repo tree depth first (a node's own
 *  sessions, then its nested worktrees), then the other-sessions section. Fold state and the
 *  rail's search box are ignored — a folded node still holds its place. */
export function railOrder(sessions: Session[], repos: Repo[]): Session[] {
  const out: Session[] = [];
  const walk = (n: RepoTreeNode) => {
    out.push(...sessionsInFolder(sessions, n.repo.name));
    n.children.forEach(walk);
  };
  repoTree(repos, sessions).forEach(walk);
  out.push(...orphanSessions(sessions, repos));
  return out;
}

const terminalKind = (s: Session) => s.kind === "shell" || s.kind === "ssm";

/** The rotation candidates, in rail order (see railOrder). set=null means all of them. */
export function rotatableSessions(order: Session[], set: WorkingSet | null): Session[] {
  return order.filter((s) => !!s.alive && !terminalKind(s) && (!set || sessionInSet(set, s)));
}

export interface RotateTarget {
  session: Session;
  /** Zero-based position of the destination (the toast renders it as "2/3"). */
  index: number;
  total: number;
}

/** The destination delta steps on from current, wrapping at either end.
 *
 * - When current is not a candidate but sits in `order` (a shell, a stopped session), step
 *   from its place there: forward lands on the first candidate below it, back on the first
 *   above it, as the rail reads.
 * - Otherwise (not a session pane at all, or in another working set) start from the head when
 *   moving forward and from the tail when moving back.
 * - When the destination would be where we already are (only one candidate), return null and
 *   do nothing. */
export function rotateTarget(
  list: Session[],
  current: string | null | undefined,
  delta: number,
  order: Session[] = list,
): RotateTarget | null {
  if (list.length === 0 || delta === 0) return null;
  const at = list.findIndex((s) => s.name === current);
  if (list.length === 1) return at === 0 ? null : { session: list[0], index: 0, total: 1 };
  let base = at;
  if (at < 0) {
    const pos = order.findIndex((s) => s.name === current);
    // Candidates above current: forward starts just before the first one below it.
    const above = pos < 0 ? 0 : list.filter((s) => order.indexOf(s) < pos).length;
    base = delta > 0 ? above - 1 : above;
  }
  // Double mod so a negative |delta| > list.length still cannot produce a negative index
  // (same as layout/nav).
  const i = (((base + delta) % list.length) + list.length) % list.length;
  return { session: list[i], index: i, total: list.length };
}

/** The session a pane stands for in the rotation. An image-studio pane has no session of its
 *  own; it stands for the session bound to its studio, or a swipe from it would start over at
 *  the head of the list — which is usually that same session, so the swipe did nothing. */
export function rotationCurrent(
  pane: { session?: string | null; content: { kind: string; studioId?: string | null } } | null | undefined,
  sessions: Session[],
): string | null {
  if (!pane) return null;
  if (pane.content.kind === "imagegen") {
    const id = pane.content.studioId;
    return (id && sessions.find((s) => s.studio === id)?.name) || null;
  }
  return pane.session || null;
}
