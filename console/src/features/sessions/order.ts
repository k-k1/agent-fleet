// Ordering of the command palette's session list — pure functions only (no clock, no store).
//
// The order runs from "waiting for this person's answer right now" to "not running", in
// three stages:
//
//   stage 0 waiting … alive and in question / plan / permission, most recently entered
//                     first. This is why the palette gets opened at all.
//   stage 1 alive   … every other live session, also by when it last went into a wait: the
//                     session just answered comes first, which makes "answer, then look at
//                     what it does next" the shortest round trip. Sessions that never waited
//                     fall back to newest first.
//   stage 2 stopped … folded or dead. Content urgency never lifts a row out of this stage,
//                     but within it a carried row (it was folded while an unanswered
//                     exchange was open) comes first — buried in the stopped pile it is
//                     exactly the row docs/log/75 exists to surface.
//
// waitingAt returns the epoch ms at which a session last entered a wait (0 = unknown); the
// caller composes it from the notification and observation ledgers
// (waitingAtFromNotifications + observedWaitingAt). Keeping the clock out of here is what
// lets the order be pinned in tests.
import { compareText } from "../../lib/intl.ts";
import { isWaiting } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

/** The stage; lower sorts higher. */
export const TIER_WAITING = 0;
export const TIER_ALIVE = 1;
export const TIER_STOPPED = 2;

export function sessionTier(s: Session): number {
  if (!s.alive) return TIER_STOPPED;
  return isWaiting(s) ? TIER_WAITING : TIER_ALIVE;
}

/** The notification kinds (GET /api/notifications) that mean "now waiting for a person". */
const WAITING_NOTIFICATIONS = new Set(["question", "plan-approval", "permission-request"]);

/** Notification ledger → per-session epoch ms of the last time it entered a wait.
 *
 * The parameter is typed structurally to avoid a circular import (the notification store
 * references the session store). Already-seen notifications count too: whether it was read
 * and when the wait began are different questions. */
export function waitingAtFromNotifications(
  items: { kind?: string; target?: { type?: string; id?: string }; createdAt?: string }[],
): Record<string, number> {
  const out: Record<string, number> = {};
  for (const n of items) {
    if (!n.kind || !WAITING_NOTIFICATIONS.has(n.kind)) continue;
    if (n.target?.type !== "session" || !n.target.id) continue;
    const at = new Date(n.createdAt || "").getTime();
    if (!isFinite(at)) continue;
    if (at > (out[n.target.id] || 0)) out[n.target.id] = at;
  }
  return out;
}

/** Rank within stage 2: folded with an unanswered exchange comes first. */
const carriedRank = (s: Session): number => (s.carried ? 0 : 1);

/** Newest first (missing values last). ISO strings sort chronologically as plain text. */
const byCreatedDesc = (a: Session, b: Session): number => compareText(b.createdAt || "", a.createdAt || "");

/** Sorts sessions into the three stages above (the input array is not modified). */
export function sortSessionsByAttention(sessions: Session[], waitingAt: (name: string) => number): Session[] {
  return [...sessions].sort((a, b) => {
    const tier = sessionTier(a) - sessionTier(b);
    if (tier) return tier;
    if (sessionTier(a) === TIER_STOPPED) {
      const carried = carriedRank(a) - carriedRank(b);
      if (carried) return carried;
    } else {
      const at = waitingAt(b.name) - waitingAt(a.name);
      if (at) return at;
    }
    return byCreatedDesc(a, b) || compareText(a.name, b.name);
  });
}
