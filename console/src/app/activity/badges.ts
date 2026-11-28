// What the activity bar's icons say about a view that is not on screen (ADR 0140).
//
// A badge is a notice, not an inventory: it marks something that arrived and that the member has
// not looked at. A view whose rows only the member adds has none; the memo queue is one only for
// the notes that wait for the member's approval, which is a waiting badge (below).
//
// Two kinds, and they clear differently:
//   - waiting: a session is stopped on the member's answer. It stays until the answer is given,
//     however often the view is opened, because looking at the row does not unblock the session.
//   - news: rows that changed while the view was off screen. Opening the view acknowledges them
//     here; the rows keep their own marks, which clear by each feature's own rule.
//
// A badge counts only rows its view lists. With a working set active that is the set's rows: a
// count the member cannot find a row for, in the view the badge sends them to, is worse than no
// count, and the set's own bar is what says the rest is out of sight. Auto-run usage counts
// occupied slots across the member's whole queue, in the neutral running tone; its waiting and
// failure cues stay scoped and are drawn as a separate dot beside the count.
import { create } from "zustand";
import type { RailView } from "../../core/store/leftRail.ts";
import type { Session } from "../../types/session.ts";
import type { WorkItem } from "../../features/workitems/read.ts";
import type { SeenMap } from "../../features/workitems/seen.ts";
import { seenId, unreadOf } from "../../features/workitems/seen.ts";
import type { FleetNotification } from "../../features/notifications/store.ts";
import { opensConversation, unreadSessionNames } from "../../features/notifications/read.ts";
import { convInSet, sessionInSet, sessionInWorkingSet } from "../../lib/workingSets.ts";
import { withoutTerminals, withoutUnattended } from "../../lib/project.ts";
import { visibleTerminals } from "../../features/terminals/visible.ts";
import type { Repo } from "../../features/repos/store.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";

export type RailBadge =
  | { tone: "waiting" | "news"; count: number; openRuns?: number }
  | { tone: "unread" | "failed"; count?: undefined; openRuns?: number }
  | { tone: "running"; count?: undefined; openRuns: number };

const WAITING = new Set(["question", "plan", "permission"]);

/** Is this running session stopped on an answer only the member can give? */
export const isWaitingSession = (s: Session): boolean => s.alive === true && WAITING.has(String(s.state || ""));

/** Running sessions stopped on an answer only the member can give. The usage-limit and
 * login states are left out: they are not answered in the session's pane. */
export function waitingSessions(sessions: Session[], set: WorkingSet | null = null, shown?: ReadonlySet<string>, repos: Repo[] = []): number {
  // Placed in the set as the views place the row (sessionInWorkingSet), or a waiting session in
  // a worktree with a folder name of its own is listed and not counted.
  const byFolder = new Map(repos.map((r) => [r.name, r]));
  return withoutUnattended(sessions, shown).filter((s) => s.alive && WAITING.has(String(s.state || "")) && (!set || sessionInWorkingSet(set, s, byFolder))).length;
}

/** Where the session badges go. The Sessions and the Active view list the same waiting
 * sessions (ADR 0145), and one session counted on two icons reads as two. While the Active
 * icon is in the bar it carries the count, being the view that is about what needs the member
 * now; otherwise the Sessions icon does, as before. `activeShown` is false while the workspace
 * is not running too: the stopped Sessions view stays where the count and the rows are, though
 * the Active view lists recent stops from the mirror then. The unread dot is the Sessions view's
 * either way: only that view lists every session a notice can be about. */
export function sessionBadges(waiting: number, unread: boolean, activeShown: boolean): Partial<Record<"sessions" | "active", RailBadge>> {
  const out: Partial<Record<"sessions" | "active", RailBadge>> = {};
  if (waiting > 0) out[activeShown ? "active" : "sessions"] = { tone: "waiting", count: waiting };
  if (unread && !out.sessions) out.sessions = { tone: "unread" };
  return out;
}

/** Is there an unseen notification on a session the Sessions view lists? Under a working set
 * a session the list does not know cannot be placed in it, so it does not count. */
export function hasUnreadSession(items: FleetNotification[], sessions: Session[], set: WorkingSet | null, withTerminals = false, shown?: ReadonlySet<string>): boolean {
  const names = unreadSessionNames(items);
  // A shell's or an SSM login's unread notice (an OSC desktop notification from a program in it)
  // is the Terminal view's: the Sessions view does not list the row, so its icon would point at
  // nothing there.
  // `withTerminals`: the stopped workspace's history lists every session, shells included.
  const listed = new Set((withTerminals ? withoutUnattended(sessions, shown) : withoutTerminals(withoutUnattended(sessions, shown))).map((s) => s.name));
  const hidden = new Set(sessions.map((s) => s.name).filter((n) => !listed.has(n)));
  if (!set) return names.some((n) => !hidden.has(n));
  const byName = new Map(sessions.map((s) => [s.name, s] as const));
  return names.some((n) => {
    const s = byName.get(n);
    return !!s && listed.has(n) && sessionInSet(set, s);
  });
}

/** Is there an unseen notification on a shell or SSM login the Terminal view lists? The same rows,
 * under the same working set, as the view itself shows and its chips count. */
export function hasUnreadTerminal(items: FleetNotification[], sessions: Session[], repos: Repo[], set: WorkingSet | null): boolean {
  const names = new Set(unreadSessionNames(items));
  return visibleTerminals(sessions, repos, set).some((s) => names.has(s.name));
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
