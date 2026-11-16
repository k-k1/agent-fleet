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
import { create } from "zustand";
import type { RailView } from "../../core/store/leftRail.ts";
import type { Session } from "../../types/session.ts";
import type { WorkItem } from "../../features/workitems/read.ts";
import type { SeenMap } from "../../features/workitems/seen.ts";
import { seenId, unreadOf } from "../../features/workitems/seen.ts";

export type RailBadge =
  | { tone: "waiting" | "news"; count: number }
  | { tone: "unread" | "failed"; count?: undefined };

const WAITING = new Set(["question", "plan", "permission"]);

/** Running sessions stopped on an answer only the member can give. The usage-limit and
 * login states are left out: they are not answered in the session's pane. */
export function waitingSessions(sessions: Session[]): number {
  return sessions.filter((s) => s.alive && WAITING.has(String(s.state || ""))).length;
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
  /** The view was on screen with exactly these rows unread. Replaces the previous list, so
   * what is kept never outgrows what is unread right now. */
  ack(view: RailView, keys: string[]): void;
}

export const useRailAck = create<RailAckStore>((set, get) => ({
  acked: readAcked(),
  ack: (view, keys) => {
    const cur = get().acked[view] || [];
    if (cur.length === keys.length && cur.every((k, i) => k === keys[i])) return;
    const acked = { ...get().acked, [view]: keys };
    try {
      localStorage.setItem(ACK_KEY, JSON.stringify(acked));
    } catch {}
    set({ acked });
  },
}));
