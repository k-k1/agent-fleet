// The sessions schedules started, listed in the Schedules view (they are not in the repository
// tree). Live runs first with their spawned sessions one level under; finished runs fold into
// "Finished N". A click opens the session.
import { useMemo, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { useActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";
import { waitsForMember } from "../autorun/runs.ts";
import type { Session } from "../../types/session.ts";
import { buildScheduledRuns, type ScheduledRun } from "./scheduledSessions.ts";

const open = (s: Session) => (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(s.name);
const dotClass = (st: string) => (st === "running" ? "run-live" : st === "waiting" ? "run-wait" : "");
const stateOf = (s: Session) => (waitsForMember(s) ? "waiting" : s.alive ? "running" : "ended");

function Item({ run }: { run: ScheduledRun }) {
  const tr = useT();
  return (
    <li className="sched-sess" data-sched-session={run.root.name}>
      {run.rootShown ? (
        <button type="button" className="sched-sess-main" data-rail-row="" onClick={() => open(run.root)} title={tr("sched.open_session", { name: run.root.name })}>
          <span className={"sched-sess-dot " + dotClass(stateOf(run.root))} aria-hidden="true" />
          <span className="sched-sess-title">{displayName(run.root)}</span>
        </button>
      ) : (
        // Outside the working set: only the heading of the children that are in it.
        <div className="sched-sess-anchor">
          <span className="sched-sess-title">{displayName(run.root)}</span>
        </div>
      )}
      {run.children.length > 0 && (
        <ul className="sched-sess-kids">
          {run.children.map((c) => (
            <li key={c.name}>
              <button type="button" className="sched-sess-main" data-rail-row="" onClick={() => open(c)} title={tr("sched.open_session", { name: c.name })}>
                <span className={"sched-sess-dot " + dotClass(stateOf(c))} aria-hidden="true" />
                <span className="sched-sess-title">{displayName(c)}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

/** The runs the Schedules view shows, newest first, narrowed by the active working set. */
export function useScheduledRuns(): ScheduledRun[] {
  const sessions = useSessionsStore((s) => s.sessions);
  const wset = useActiveWorkingSet();
  const repos = useReposStore((s) => s.repos);
  return useMemo(() => buildScheduledRuns(sessions, wset, repos), [sessions, wset, repos]);
}

/** Live runs, then the finished ones folded under "Finished N". */
export function ScheduledRunList({ runs }: { runs: ScheduledRun[] }) {
  const tr = useT();
  const [showEnded, setShowEnded] = useState(false);
  if (runs.length === 0) return null;
  const live = runs.filter((r) => r.live);
  const ended = runs.filter((r) => !r.live);
  return (
    <>
      <ul className="sched-sess-list">
        {live.map((r) => (
          <Item key={r.root.name} run={r} />
        ))}
      </ul>
      {ended.length > 0 && (
        <>
          <button type="button" className="sched-sess-ended" data-rail-row="" aria-expanded={showEnded} onClick={() => setShowEnded((v) => !v)}>
            <Icon name={showEnded ? "chevron-down" : "chevron-right"} />
            {tr("sched.sessions_ended", { n: ended.length })}
          </button>
          {showEnded && (
            <ul className="sched-sess-list">
              {ended.map((r) => (
                <Item key={r.root.name} run={r} />
              ))}
            </ul>
          )}
        </>
      )}
    </>
  );
}

/** The runs that belong under no listed schedule: a deleted or hidden one, or a session that
 *  carries no schedule id. */
export function ScheduledSessions({ runs }: { runs: ScheduledRun[] }) {
  const tr = useT();
  if (runs.length === 0) return null;
  return (
    <section className="sched-sess-group" data-sched-sessions="">
      <h3 className="sched-sess-head">{tr("sched.sessions")}</h3>
      <ScheduledRunList runs={runs} />
    </section>
  );
}
