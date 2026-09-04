// Rotation across running sessions — the selection rule behind the phone's swipe gesture
// (app/App.tsx).
//
// This module is PURE (no store, no DOM) so it can be unit-tested in the node vitest
// project; the side-effecting part that actually opens a pane lives in sessions/open.ts
// (the same split as workingSets.ts vs workingSetsStore.ts).
//
// Candidates and order (respecting the working sets of docs/log/52):
// - Only alive sessions. A stopped one is not a switch target; it needs a decision to resume.
// - When a working set is selected, follow that filter, so the set matches what the left rail
//   shows.
// - The order is whatever GET /api/sessions returned (CreatedAt descending = newest first,
//   session_handlers.go). As long as the list does not change, the rotation order is stable.
import { sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

/** The rotation candidates. set=null means all of them (no filter). */
export function rotatableSessions(sessions: Session[], set: WorkingSet | null): Session[] {
  return sessions.filter((s) => !!s.alive && (!set || sessionInSet(set, s)));
}

export interface RotateTarget {
  session: Session;
  /** Zero-based position of the destination (the toast renders it as "2/3"). */
  index: number;
  total: number;
}

/** The destination delta steps on from current, wrapping at either end.
 *
 * - When current is not a candidate (stopped, in another working set, or not a session pane
 *   at all), start from the head when moving forward and from the tail when moving back.
 * - When the destination would be where we already are (only one candidate), return null and
 *   do nothing. */
export function rotateTarget(
  list: Session[],
  current: string | null | undefined,
  delta: number,
): RotateTarget | null {
  if (list.length === 0 || delta === 0) return null;
  const at = list.findIndex((s) => s.name === current);
  if (list.length === 1) return at === 0 ? null : { session: list[0], index: 0, total: 1 };
  // Base when current is not in the list: -1 going forward (→ head), 0 going back (→ tail).
  const base = at < 0 ? (delta > 0 ? -1 : 0) : at;
  // Double mod so a negative |delta| > list.length still cannot produce a negative index
  // (same as layout/nav).
  const i = (((base + delta) % list.length) + list.length) % list.length;
  return { session: list[i], index: i, total: list.length };
}
