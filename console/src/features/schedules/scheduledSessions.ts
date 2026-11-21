// Sessions a schedule started (origin "schedule") and the ones they spawned, as the Schedules
// view lists them. They are in no other view: the repository tree and the Sessions list leave
// unattended work out. A session carries no schedule id, so they are listed as a group of their
// own, newest first, not under one schedule.
import type { Session } from "../../types/session.ts";
import { waitsForMember } from "../autorun/runs.ts";

export interface ScheduledRun {
  root: Session;
  /** Sessions the run spawned (origin "session" all the way up), oldest first. */
  children: Session[];
  /** Is any session of the run alive or waiting on the member? An ended run is folded away. */
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

export function buildScheduledRuns(sessions: Session[]): ScheduledRun[] {
  const byName = new Map(sessions.map((s) => [s.name, s]));
  const kids = new Map<string, Session[]>();
  for (const s of sessions) {
    if (s.origin !== "session") continue;
    const root = scheduleRootName(s, byName);
    if (!root) continue;
    kids.set(root, [...(kids.get(root) ?? []), s]);
  }
  return sessions
    .filter((s) => s.origin === "schedule")
    .sort((a, b) => byCreated(b, a))
    .map((root) => {
      const children = (kids.get(root.name) ?? []).sort(byCreated);
      const tree = [root, ...children];
      const waiting = tree.some(waitsForMember);
      return { root, children, waiting, live: waiting || tree.some((s) => s.alive) };
    });
}

/** Sessions of scheduled runs that wait for the member's answer: the Schedules icon's count. */
export const waitingScheduledCount = (sessions: Session[]): number =>
  buildScheduledRuns(sessions).reduce((n, r) => n + [r.root, ...r.children].filter((s) => s.alive && waitsForMember(s)).length, 0);
