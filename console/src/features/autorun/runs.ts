// The rows of the Auto-run view: one per run, not per worktree. A run is a session an
// auto-run queue started (origin "queue"); the sessions it spawned (reviewers, origin
// "session" all the way up) hang under it.
import type { Session, SessionPR, SessionWorkLink } from "../../types/session.ts";
import { isWaitingSession } from "../../app/activity/badges.ts";
import type { MemoQueue } from "../../types/memo.ts";
import type { WorkingSet } from "../../lib/workingSets.ts";

export type RunState = "running" | "waiting" | "ended";

/** What a queue reported about the run that started a session. */
export interface RunDelivery {
  id: string;
  categoryId: string;
  state: string;
  /** How a closed run ended: clean | aborted | ended | unknown | not_sent | deferred. */
  outcome: string;
  /** How the run's first turn ended: clean | turn-failed | ""; the only failure signal a queue records. */
  turnOutcome: string;
  sentAt: string;
  createdAt: string;
  closedAt: string;
  /** Why an open run keeps its place at the queue's last report ("person": it waits for the member). */
  heldBy: string;
}

export interface RunRow {
  root: Session;
  /** The queue's category id the run came from: the delivery's, else the session's own; "" when neither names one. */
  categoryId: string;
  state: RunState;
  /** The delivery that started the run; "" while unknown. */
  deliveryId: string;
  /** The queue closed the run and its first turn failed. A run the member ended or aborted is not one. */
  failed: boolean;
  /** The queue closed the run but its root session is alive again (the member took it over or
   *  started it again): listed as running, tagged so it is not mistaken for a fresh run. */
  resumed: boolean;
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

/** Runs that wait for the member's answer: the Auto-run icon's count. */
export const waitingRunCount = (runs: RunRow[]): number => runs.filter((r) => r.state === "waiting").length;

/** One key per failed run: the Auto-run icon's red dot, acknowledged by opening the view. */
export const failedRunKeys = (runs: RunRow[]): string[] => runs.filter((r) => r.failed && r.deliveryId).map((r) => r.deliveryId);

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
 *  while a session it started still works, and closed once it ended), except that a closed run
 *  whose root session is alive is listed as running (`resumed`); the sessions' own state decides
 *  only when no delivery is known. */
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
      let resumed = false;
      if (d && d.state === "closed") {
        // Closed is the queue's word about the delivery; a root that works again is still work.
        resumed = root.alive === true;
        state = resumed ? (waitsForMember(root) ? "waiting" : "running") : "ended";
      }
      else if (d && OPEN.has(d.state)) state = waiting ? "waiting" : "running";
      // No usable delivery (none read, or its state expired): the sessions' own word.
      else if (!tree.some((x) => x.alive) && !waiting) state = "ended";
      else state = waiting ? "waiting" : "running";
      const startedAt = ms(d?.sentAt) || ms(d?.createdAt) || ms(root.createdAt);
      return {
        root,
        // The delivery's word first; a run whose delivery left the history window has the session's own.
        categoryId: d?.categoryId ?? root.queueCategoryId ?? "",
        deliveryId: d?.id ?? "",
        // From the delivery alone: resuming the run's session changes what the row shows, not what happened.
        failed: d?.state === "closed" && d.turnOutcome === "turn-failed" && d.outcome !== "ended" && d.outcome !== "aborted",
        state,
        resumed,
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

/** The runs a working set shows: a category is in the set when its repository is; a category without
 *  a repository, and the runs no queue category names, are in every set. The view, the icon's count
 *  and the failure acknowledgement all use this one rule. */
export function scopeRuns(runs: RunRow[], queues: MemoQueue[], set: WorkingSet | null): RunRow[] {
  if (!set) return runs;
  return runs.filter((r) => {
    const q = queues.find((x) => x.categoryId === r.categoryId);
    return !q || !q.repo || set.repos.includes(q.repo);
  });
}
