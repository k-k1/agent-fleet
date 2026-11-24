// Sessions a schedule started (origin "schedule") and the ones they spawned, as the Schedules
// view lists them. They are in no other view: the repository tree and the Sessions list leave
// unattended work out. A run is listed under its schedule's row by the session's scheduleId; one
// whose schedule is not listed (deleted, hidden by the working set) or that carries no id stays in
// a group of its own, newest first.
import type { Session } from "../../types/session.ts";
import { waitsForMember } from "../autorun/runs.ts";
import { repoInSet, sessionInSet, type WorkingSet } from "../../lib/workingSets.ts";
import { sessionAutoKind, sessionFolder } from "../../lib/project.ts";
import type { Repo } from "../repos/store.ts";

export interface ScheduledRun {
  root: Session;
  /** The schedule that started the run ("" when the root carries none). */
  scheduleId: string;
  /** Sessions the run spawned (origin "session" all the way up), oldest first; under a working set,
   *  only those in it. */
  children: Session[];
  /** False when the working set hides the root: it stays only as the heading of the children it shows,
   *  with no state of its own and nothing to open. */
  rootShown: boolean;
  /** Is any session of the run that the working set shows alive or waiting on the member? An ended
   *  run is folded away. */
  live: boolean;
  waiting: boolean;
}

const byCreated = (a: Session, b: Session) => (a.createdAt || "").localeCompare(b.createdAt || "");

/** The schedule-started run a session belongs to: the origin=schedule session reached by
 *  following origin=session edges. "" for anything else, a missing parent and a cycle included. */
function scheduleRootName(s: Session, byName: Map<string, Session>): string {
  const seen = new Set<string>();
  let cur: Session | undefined = s;
  while (cur) {
    if (seen.has(cur.name)) return "";
    seen.add(cur.name);
    if (cur.origin === "schedule") return cur.name;
    if (cur.origin !== "session") return "";
    cur = cur.originSession ? byName.get(cur.originSession) : undefined;
  }
  return "";
}

/** Is a session in the working set? As the tree decided it: a session assigned by name, or one in a
 *  working copy whose base repository is in the set (a worktree with a custom folder name belongs to
 *  its `parent`). Without the repo list the folder name decides. */
export function scheduledSessionInSet(set: WorkingSet, s: Session, repos: Repo[]): boolean {
  if (set.sessions.includes(s.name)) return true;
  const folder = sessionFolder(s);
  const repo = folder ? repos.find((r) => r.name === folder) : undefined;
  return repo ? repoInSet(set, repo) : sessionInSet(set, s);
}

/** `set` narrows the runs the way the tree used to: a run is shown when its root or any session it
 *  started is in the set (the root then anchors a child that is). */
export function buildScheduledRuns(sessions: Session[], set: WorkingSet | null = null, repos: Repo[] = []): ScheduledRun[] {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const kids = new Map<string, Session[]>();
  for (const s of sessions) {
    if (s.origin !== "session") continue;
    const root = scheduleRootName(s, byName);
    if (!root) continue;
    kids.set(root, [...(kids.get(root) ?? []), s]);
  }
  const shows = (s: Session) => !set || scheduledSessionInSet(set, s, repos);
  return sessions
    .filter((s) => s.origin === "schedule")
    .filter((s) => [s, ...(kids.get(s.name) ?? [])].some(shows))
    .sort((a, b) => byCreated(b, a))
    .map((root) => {
      const children = (kids.get(root.name) ?? []).filter(shows).sort(byCreated);
      // A root outside the set stays as the anchor of a child that is in it, but its own state is
      // not what the set shows.
      const shown = [...(shows(root) ? [root] : []), ...children];
      const waiting = shown.some(waitsForMember);
      return { root, scheduleId: root.scheduleId || "", children, rootShown: shows(root), waiting, live: waiting || shown.some((s) => s.alive) };
    });
}

/** Sessions of scheduled runs that wait for the member's answer, by the rule the list uses
 *  (a stopped session that still carries its question counts), and that the working set shows:
 *  the Schedules icon's count. A child the Sessions view lists too (`shown`, see sessionAutoKind)
 *  counts on that icon instead, not on both. */
export const waitingScheduledCount = (sessions: Session[], set: WorkingSet | null = null, repos: Repo[] = [], shown?: ReadonlySet<string>): number => {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  return buildScheduledRuns(sessions, set, repos).reduce(
    (n, r) =>
      n +
      [r.root, ...r.children].filter((s) => waitsForMember(s) && (!set || scheduledSessionInSet(set, s, repos)) && sessionAutoKind(s, byName, shown) !== "").length,
    0,
  );
};

/** Splits runs (newest first, as built) into those under a listed schedule, by id, and the rest. */
export function groupRunsBySchedule(runs: ScheduledRun[], known: ReadonlySet<string>): { bySchedule: Map<string, ScheduledRun[]>; rest: ScheduledRun[] } {
  const bySchedule = new Map<string, ScheduledRun[]>();
  const rest: ScheduledRun[] = [];
  for (const r of runs) {
    if (!r.scheduleId || !known.has(r.scheduleId)) {
      rest.push(r);
      continue;
    }
    const bucket = bySchedule.get(r.scheduleId);
    if (bucket) bucket.push(r);
    else bySchedule.set(r.scheduleId, [r]);
  }
  return { bySchedule, rest };
}
