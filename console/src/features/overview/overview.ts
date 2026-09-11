// Which sessions the overview shows and in what order — pure functions only (no store, no
// clock), so the node vitest project pins them.
//
// Scope: alive sessions, and stopped ones only when the pane's toggle asks for them; the
// active working set narrows both, so the grid matches what the left rail shows (the same
// rule as the phone's session rotation, rotate.ts).
//
// Order: the palette's three stages (waiting for a person → running → stopped), but inside
// a stage newest first and NOTHING else. The palette also sorts by "when did it last start
// waiting", which reorders rows every time a session asks a question and is answered — fine
// for a list that freezes its order the moment it opens, wrong for a grid that stays open:
// a card that keeps changing its square cannot be watched. Entering or leaving a wait is the
// one move worth the jump, because that is the card the person has to go to next.
import { sortSessionsByAttention } from "../sessions/order.ts";
import { sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";
import type { Session } from "../../types/session.ts";

const noWaitingAt = (): number => 0;

/** The cards, in grid order. set=null means every session (no filter). */
export function overviewSessions(sessions: Session[], set: WorkingSet | null, showStopped: boolean): Session[] {
  const scoped = sessions.filter((s) => (showStopped || !!s.alive) && (!set || sessionInSet(set, s)));
  return sortSessionsByAttention(scoped, noWaitingAt);
}

/** How many of the cards are running — the head's count, so it says the same thing the
 * grid shows even while stopped rows are mixed in. */
export const aliveCount = (sessions: Session[]): number => sessions.filter((s) => !!s.alive).length;
