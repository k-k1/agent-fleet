// "Needs you": the sessions the jump-to-next command (session.nextAttention) walks — pure
// functions only (no clock, no store), so the queue and the walk can be pinned in tests.
//
// There is no unread store of its own. A session needs you when either holds:
//
//   waiting … alive and in question / plan / permission (waiting.ts's isWaiting). Clears
//             when the question is answered, not when it is looked at: seeing a question is
//             not answering it.
//   unread  … the notification center still holds an unseen notification for it (a finished
//             turn, a report, …). Clears when the session is shown in a visible pane
//             (wireNotificationReadOnVisibleSessions), so the jump itself acknowledges it.
//
// Working sets are deliberately ignored: a question hidden by the rail's filter is exactly
// the one that gets missed.
import { compareText } from "../../lib/intl.ts";
import { isWaiting } from "./waiting.ts";
import type { Session } from "../../types/session.ts";

/** Notification ledger → per-session epoch ms of the newest UNSEEN session notification.
 *  Typed structurally, like order.ts's waitingAtFromNotifications, to keep the notification
 *  store out of the import graph. */
export function unreadAtFromNotifications(
  items: { seen?: boolean; target?: { type?: string; id?: string }; createdAt?: string }[],
): Record<string, number> {
  const out: Record<string, number> = {};
  for (const n of items) {
    if (n.seen || n.target?.type !== "session" || !n.target.id) continue;
    const at = new Date(n.createdAt || "").getTime();
    const id = n.target.id;
    // An unparsable time still counts as unread; it only sorts last.
    out[id] = Math.max(out[id] ?? 0, isFinite(at) ? at : 0);
  }
  return out;
}

/** The queue, in the order a first press visits it: waiting sessions, most recently entered
 *  first; then unread-only sessions, newest notification first. A session is listed once. */
export function attentionQueue(
  sessions: Session[],
  waitingAt: (name: string) => number,
  unreadAt: Record<string, number>,
): Session[] {
  const waiting = sessions.filter(isWaiting);
  const named = new Set(waiting.map((s) => s.name));
  const unread = sessions.filter((s) => !named.has(s.name) && s.name in unreadAt);
  const newestFirst = (at: (s: Session) => number) => (a: Session, b: Session) => at(b) - at(a) || compareText(a.name, b.name);
  return [
    ...waiting.sort(newestFirst((s) => waitingAt(s.name))),
    ...unread.sort(newestFirst((s) => unreadAt[s.name])),
  ];
}

/** Where the previous press landed and the order it was walking. */
export interface AttentionWalk {
  order: string[];
  at: string;
}

/** The next session to jump to, and the walk to remember for the following press.
 *
 * Re-deriving the queue on every press is not enough on its own: the jump marks an unread
 * session read, so it drops out of the queue, and "the one after the current session" would
 * restart from the head and never reach the tail. So while the active session is still the
 * one the last press landed on, the walk continues along the previous order (skipping entries
 * that no longer need you, then taking new arrivals) instead of the fresh one. Any other
 * active session starts a new walk from the fresh queue — after that session when it is in
 * the queue, from the head otherwise.
 *
 * `currentDone` false: the current session is still in the queue for a destination that is
 * not on screen (its report's conversation, say), so it is a stop too — the last one, after
 * every other.
 *
 * null when nothing needs you other than the current session. */
export function nextAttention(
  queue: string[],
  current: string | null | undefined,
  prev: AttentionWalk | null,
  currentDone = true,
): AttentionWalk | null {
  const live = new Set(queue);
  const base = prev && current && prev.at === current ? prev.order : queue;
  const kept = new Set(base);
  const order = [...base.filter((n) => live.has(n) || n === current), ...queue.filter((n) => !kept.has(n))];
  const from = current ? order.indexOf(current) : -1;
  for (let i = 1; i <= order.length; i++) {
    const n = order[(from + i) % order.length];
    if (live.has(n) && (n !== current || !currentDone)) return { order, at: n };
  }
  return null;
}
