// Ledger of "when this session last started waiting on a human". It exists only to order the
// command palette's session list by most-recently-waiting; nothing else may use it.
//
// Why a ledger is needed: GET /api/sessions returns the state (question / plan / permission)
// but not when it was entered, and that timestamp is the only basis for the ordering. So the
// console combines two sources (order.ts's waitingAt merges them):
//
//   1. The notification ledger (server side, with createdAt) — the base that survives a reload
//      and is shared across devices.
//   2. Transitions observed in this tab (this file) — fills the gaps notifications leave:
//      agents that emit no notification, questions older than the notification retention
//      window, and the second round of "question → answer → question again", where only the
//      first notification remains and the order would stay stale.
//
// Observations live in localStorage only and are never sent to the server: this is a device-
// local ordering detail, not a user setting, and losing it only falls back to the notification
// ledger's ordering.
import type { Session } from "../../types/session.ts";

const KEY = "af.sessionWaitingAt";

/** States that are waiting on a human answer. Waiting on the clock (limited) and work in
 *  progress are excluded: mixing in anything not waiting on a person stops the top of the
 *  palette from being the rows to answer right now. */
export const WAITING_STATES = new Set(["question", "plan", "permission"]);

/** Live AND waiting on a human. A stopped session's carried question (the one it held when it
 *  was folded away) does not count: answering it does not move anything now, so it belongs in
 *  the stopped tier instead (order.ts). */
export const isWaiting = (s: { alive?: boolean; state?: string }): boolean =>
  !!s.alive && WAITING_STATES.has(s.state || "");

// name -> epoch ms. null = not yet read from localStorage (read on first access).
let observed: Record<string, number> | null = null;
// Was it waiting at the previous observation? Never-observed is absent, not false, and that
// distinction is what makes the transition detection work (see the note on noteSessions).
let before: Record<string, boolean> = {};

function read(): Record<string, number> {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return {};
    const parsed: unknown = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const out: Record<string, number> = {};
    for (const [k, v] of Object.entries(parsed as Record<string, unknown>)) {
      if (typeof v === "number" && isFinite(v) && v > 0) out[k] = v;
    }
    return out;
  } catch {
    return {}; // corrupt value, or no localStorage (node tests) — only the ordering is lost
  }
}

function write(map: Record<string, number>): void {
  try {
    localStorage.setItem(KEY, JSON.stringify(map));
  } catch {
    /* private mode / quota — this only assists the ordering; losing it breaks nothing */
  }
}

/** When this device last observed the session entering a waiting state (epoch ms). 0 = never
 *  observed. */
export function observedWaitingAt(name: string): number {
  if (!observed) observed = read();
  return observed[name] || 0;
}

/** Call on every arriving session list (both poll and push go through applyList).
 *
 * A first observation that is already waiting is NOT recorded: we do not know when it entered,
 * and burning `now` would give every session the same timestamp right after a reload, so this
 * fake time would override the real order held by the notification ledger (the merge takes the
 * max, so the newer — fake — value wins). */
export function noteSessions(list: Session[], now: number = Date.now()): void {
  // An empty list does not mean "everything is gone": it is also the value at startup, and the
  // store keeps the last list on a fetch failure (see refresh in store.ts). Pruning here would
  // throw the ordering away for nothing.
  if (!list.length) return;
  if (!observed) observed = read();
  let changed = false;
  const next: Record<string, boolean> = {};
  const live = new Set<string>();
  for (const s of list) {
    live.add(s.name);
    const waiting = isWaiting(s);
    next[s.name] = waiting;
    if (before[s.name] === false && waiting) {
      observed[s.name] = now;
      changed = true;
    }
  }
  before = next;
  // Drop sessions that left the list (deleted or archived), capping the ledger at the number
  // of live sessions.
  for (const name of Object.keys(observed)) {
    if (!live.has(name)) {
      delete observed[name];
      changed = true;
    }
  }
  if (changed) write(observed);
}

/** Test seam: discard the module's observation state. */
export function resetWaitingLedgerForTest(): void {
  observed = null;
  before = {};
}
