// What the activity bar's icons say about a view that is not on screen (ADR 0140).
//
// A badge is a notice, not an inventory: it marks something that arrived and that the member has
// not looked at. A view whose rows only the member adds (the memo queue) therefore has none.
//
// Two kinds, and they clear differently:
//   - waiting: a session is stopped on the member's answer. It stays until the answer is given,
//     however often the view is opened, because looking at the row does not unblock the session.
//   - news: rows that changed while the view was off screen. Opening the view acknowledges them
//     here; the rows keep their own marks, which clear by each feature's own rule.
//
// A badge counts only rows its view lists. With a working set active that is the set's rows: a
// count the member cannot find a row for, in the view the badge sends them to, is worse than no
// count, and the set's own bar is what says the rest is out of sight.
import { create } from "zustand";
import type { RailView } from "../../core/store/leftRail.ts";
import type { Session } from "../../types/session.ts";
import type { WorkItem } from "../../features/workitems/read.ts";
import type { SeenMap } from "../../features/workitems/seen.ts";
import { seenId, unreadOf } from "../../features/workitems/seen.ts";
import type { FleetNotification } from "../../features/notifications/store.ts";
import { opensConversation, unreadSessionNames } from "../../features/notifications/read.ts";
import { convInSet, sessionInSet } from "../../lib/workingSets.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";

export type RailBadge =
  | { tone: "waiting" | "news"; count: number }
  | { tone: "unread" | "failed"; count?: undefined };

const WAITING = new Set(["question", "plan", "permission"]);

/** Running sessions stopped on an answer only the member can give. The usage-limit and
 * login states are left out: they are not answered in the session's pane. */
export function waitingSessions(sessions: Session[], set: WorkingSet | null = null): number {
  return sessions.filter((s) => s.alive && WAITING.has(String(s.state || "")) && (!set || sessionInSet(set, s))).length;
}

/** Is there an unseen notification on a session the Sessions view lists? Under a working set
 * a session the list does not know cannot be placed in it, so it does not count. */
export function hasUnreadSession(items: FleetNotification[], sessions: Session[], set: WorkingSet | null): boolean {
  const names = unreadSessionNames(items);
  if (!set) return names.length > 0;
  const byName = new Map(sessions.map((s) => [s.name, s] as const));
  return names.some((n) => {
    const s = byName.get(n);
    return !!s && sessionInSet(set, s);
  });
}

/** Is there an unseen notice that opens a conversation the Assistants view lists? */
export function hasUnreadConversation(items: FleetNotification[], set: WorkingSet | null): boolean {
  return items.some((n) => !n.seen && opensConversation(n) && (!set || convInSet(set, String(n.payload.conversation_id))));
}

/** One key per unread row, carrying what made it unread, so a row that moves again after it
 * was acknowledged counts again. */
export function workItemNewsKeys(items: WorkItem[], seen: SeenMap): string[] {
  return items.filter((i) => unreadOf(seen, i) !== "").map((i) => `${seenId(i)}@${i.updatedAt}#${i.commentCount ?? ""}`);
}

export const pendingNews = (keys: string[], acked: readonly string[] | undefined): string[] => {
  if (!acked || acked.length === 0) return keys;
  const known = new Set(acked);
  return keys.filter((k) => !known.has(k));
};

/** What stays acknowledged: the earlier keys that still exist, plus the ones shown now. */
export function mergeAcked(prev: readonly string[], shown: readonly string[], live: readonly string[]): string[] {
  const alive = new Set(live);
  const out = prev.filter((k) => alive.has(k));
  const have = new Set(out);
  for (const k of shown) if (!have.has(k)) out.push(k);
  return out;
}

const ACK_KEY = "af-rail-acked";
type Acked = Partial<Record<RailView, string[]>>;

function readAcked(): Acked {
  try {
    const v: unknown = JSON.parse(localStorage.getItem(ACK_KEY) || "{}");
    if (!v || typeof v !== "object" || Array.isArray(v)) return {};
    const out: Acked = {};
    for (const [k, list] of Object.entries(v as Record<string, unknown>)) {
      if (Array.isArray(list)) out[k as RailView] = list.filter((x): x is string => typeof x === "string");
    }
    return out;
  } catch {
    return {};
  }
}

interface RailAckStore {
  acked: Acked;
  /** The view was on screen listing `shown`. They join what was acknowledged before, and
   * whatever is no longer in `live` is dropped: `live` is every key the view could list with
   * nothing narrowing it, so the kept list never outgrows what exists, while narrowing the
   * view (a working set) does not forget what was acknowledged outside it. */
  ack(view: RailView, shown: string[], live?: string[]): void;
}

export const useRailAck = create<RailAckStore>((set, get) => ({
  acked: readAcked(),
  ack: (view, shown, live = shown) => {
    const cur = get().acked[view] || [];
    const keys = mergeAcked(cur, shown, live);
    if (cur.length === keys.length && cur.every((k, i) => k === keys[i])) return;
    const acked = { ...get().acked, [view]: keys };
    try {
      localStorage.setItem(ACK_KEY, JSON.stringify(acked));
    } catch {}
    set({ acked });
  },
}));
