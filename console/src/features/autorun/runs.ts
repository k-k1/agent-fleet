// The rows of the Auto-run view: one per run, not per worktree. A run is a session an
// auto-run queue started (origin "queue"); the sessions it spawned (reviewers, origin
// "session" all the way up) hang under it.
import type { Session, SessionPR, SessionWorkLink } from "../../types/session.ts";
import type { MemoDelivery } from "../../types/memo.ts";
import { isWaitingSession } from "../../app/activity/badges.ts";

export type RunState = "running" | "waiting" | "ended";

export interface RunRow {
  root: Session;
  /** The queue's category id the run came from; "" when no delivery names this session. */
  categoryId: string;
  state: RunState;
  /** The work item the run started for; null when the session links none. */
  item: { key: string; label: string; title: string } | null;
  pr: SessionPR | null;
  /** Sessions the run started, oldest first. */
  children: Session[];
}

/** The run a session belongs to: the origin=queue session reached by following origin=session
 *  edges. "" for anything else, a missing parent and a cycle included. */
export function runRootName(s: Session, byName: Map<string, Session>): string {
  const seen = new Set<string>();
  let cur: Session | undefined = s;
  while (cur) {
    if (seen.has(cur.name)) return "";
    seen.add(cur.name);
    if (cur.origin === "queue") return cur.name;
    if (cur.origin !== "session") return "";
    cur = cur.originSession ? byName.get(cur.originSession) : undefined;
  }
  return "";
}

/** "owner/repo#45" -> "#45"; a Jira key stays as it is. */
export function itemLabel(key: string): string {
  const i = key.lastIndexOf("#");
  return i >= 0 ? key.slice(i) : key;
}

function startedLink(links: SessionWorkLink[] | undefined): SessionWorkLink | null {
  if (!links || links.length === 0) return null;
  return links.find((l) => l.role === "started") ?? links[0];
}

const byCreated = (a: Session, b: Session) => (a.createdAt || "").localeCompare(b.createdAt || "");

/** Every run in the session list, newest first. `categoryOf` maps a run's session name to its
 *  queue category id (from the deliveries the queues report). */
export function buildRuns(sessions: Session[], categoryOf: Map<string, string>): RunRow[] {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const kids = new Map<string, Session[]>();
  for (const s of sessions) {
    if (s.origin !== "session") continue;
    const root = runRootName(s, byName);
    if (!root) continue;
    const list = kids.get(root) ?? [];
    list.push(s);
    kids.set(root, list);
  }
  return sessions
    .filter((s) => s.origin === "queue")
    .sort((a, b) => byCreated(b, a))
    .map((root) => {
      const link = startedLink(root.links);
      return {
        root,
        categoryId: categoryOf.get(root.name) ?? "",
        state: !root.alive ? "ended" : isWaitingSession(root) ? "waiting" : "running",
        item: link ? { key: link.key, label: itemLabel(link.key), title: link.title ?? "" } : null,
        pr: root.pr ?? null,
        children: (kids.get(root.name) ?? []).sort(byCreated),
      } satisfies RunRow;
    });
}

/** session name -> category id, from each category's deliveries. */
export function categoryIndex(byCategory: Record<string, MemoDelivery[]>): Map<string, string> {
  const out = new Map<string, string>();
  for (const [id, list] of Object.entries(byCategory)) for (const d of list) if (d.session) out.set(d.session, id);
  return out;
}

/** "12m", "1h 5m", "2d 3h": how long a run has been going (or went). */
export function fmtElapsed(ms: number): string {
  const m = Math.max(0, Math.floor(ms / 60000));
  if (m < 1) return "<1m";
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  return `${Math.floor(h / 24)}d ${h % 24}h`;
}
