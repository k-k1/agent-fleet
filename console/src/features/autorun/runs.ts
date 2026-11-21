// The rows of the Auto-run view: one per run, not per worktree. A run is a session an
// auto-run queue started (origin "queue"); the sessions it spawned (reviewers, origin
// "session" all the way up) hang under it.
import type { Session, SessionPR, SessionWorkLink } from "../../types/session.ts";
import { isWaitingSession } from "../../app/activity/badges.ts";

export type RunState = "running" | "waiting" | "ended";

/** What a queue reported about the run that started a session. */
export interface RunDelivery {
  categoryId: string;
  state: string;
  sentAt: string;
  createdAt: string;
  closedAt: string;
  /** Why an open run keeps its place at the queue's last report ("person": it waits for the member). */
  heldBy: string;
}

export interface RunRow {
  root: Session;
  /** The queue's category id the run came from; "" when no delivery names this session. */
  categoryId: string;
  state: RunState;
  /** The work item the run started for; null when the session has no `started` link. */
  item: { key: string; label: string; title: string } | null;
  pr: SessionPR | null;
  /** Sessions the run started, oldest first. */
  children: Session[];
  /** When the run began and, once ended, finished (ms since the epoch); NaN when unknown. */
  startedAt: number;
  endedAt: number;
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

// Only the link the run was started for: a work item linked later (link_work_item, role
// watching) is not what the run is about.
const startedLink = (links: SessionWorkLink[] | undefined): SessionWorkLink | null => links?.find((l) => l.role === "started") ?? null;

const byCreated = (a: Session, b: Session) => (a.createdAt || "").localeCompare(b.createdAt || "");
const ms = (iso: string | undefined): number => (iso ? Date.parse(iso) : NaN);
const OPEN = new Set(["claimed", "sending", "open"]);
/** A session waiting for the member: its live state, or the question it was stopped on. */
export const waitsForMember = (s: Session): boolean => isWaitingSession(s) || (!s.alive && !!s.carried);

/** Every run in the session list, newest first. `deliveries` maps a run's session name to what
 *  its queue reported. A run's state is the queue's word when there is one (a delivery stays open
 *  while a session it started still works, and closed once it ended whatever the root is doing),
 *  and the sessions' own state only when no delivery is known. */
export function buildRuns(sessions: Session[], deliveries: Map<string, RunDelivery>): RunRow[] {
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
      const children = (kids.get(root.name) ?? []).sort(byCreated);
      const tree = [root, ...children];
      const d = deliveries.get(root.name);
      const waiting = tree.some(waitsForMember) || (!!d && OPEN.has(d.state) && d.heldBy === "person");
      let state: RunState;
      if (d && d.state === "closed") state = "ended";
      else if (d && OPEN.has(d.state)) state = waiting ? "waiting" : "running";
      // No usable delivery (none read, or its state expired): the sessions' own word.
      else if (!tree.some((x) => x.alive) && !waiting) state = "ended";
      else state = waiting ? "waiting" : "running";
      const startedAt = ms(d?.sentAt) || ms(d?.createdAt) || ms(root.createdAt);
      return {
        root,
        categoryId: d?.categoryId ?? "",
        state,
        item: link ? { key: link.key, label: itemLabel(link.key), title: link.title ?? "" } : null,
        pr: root.pr ?? null,
        children,
        startedAt,
        endedAt: state === "ended" ? ms(d?.closedAt) : NaN,
      } satisfies RunRow;
    });
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
