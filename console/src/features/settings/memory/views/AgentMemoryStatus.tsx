// AgentMemoryStatus — the one status header of the Agent memory entry: is it on, how much is in
// it, when it last changed, and whether the background sync needs attention. It only reads
// endpoints the panels below already call (entries/list, entries/changes, auto-sync); nothing
// here is new API. A read that fails leaves its figure out rather than showing a wrong zero.

import { useCallback, useEffect, useState } from "react";
import { api, isTransientErr } from "../../../../core/api/client.ts";
import { useRetryLoad } from "../../../../lib/retryLoad.ts";
import { useSettings } from "../../../../lib/settings.ts";
import { useT } from "../../../../lib/i18n/index.ts";
import { fmtDateTime, DATETIME_FULL } from "../../../../lib/intl.ts";
import type { AutoSyncStatus, MemoryChange, MemoryListed } from "../memoryTypes.ts";

interface Figures {
  count: number | null;
  projects: number | null;
  lastChange: string | null;
  syncProblem: boolean;
  /** The reconciler is still catching up with the switch, or a failure is waiting to be fixed. */
  watching: boolean;
}

const POLL_MS = 4000;

// The reconciler needs the member when a run failed, the Agent reported a problem it cannot fix
// by itself, or the last run left conflicts or skipped secrets.
function needsAttention(st: AutoSyncStatus | undefined): boolean {
  if (!st) return false;
  const left = (st.run?.projects ?? []).reduce((n, p) => n + (p.counts.conflict ?? 0) + (p.counts.secret ?? 0), 0);
  return !!st.failing || (st.problems ?? []).length > 0 || left > 0;
}

export function AgentMemoryStatus({
  running,
  reload,
  view,
  onOpenSettings,
}: {
  running: boolean;
  reload: number;
  /** The view showing: moving between views re-reads, so the header never disagrees with the list under it. */
  view: string;
  onOpenSettings: () => void;
}) {
  const tr = useT();
  const enabled = useSettings().agentMemory;
  const [fig, setFig] = useState<Figures | null>(null);
  const [tick, setTick] = useState(0);

  const load = useCallback(
    async (signal: AbortSignal) => {
      // Nothing to ask while stopped; the deps re-run this once the workspace is up.
      if (!running) return true;
      const [list, changes, sync] = await Promise.all([
        api("api/agents/memory/entries/list"),
        api("api/agents/memory/entries/changes?limit=1"),
        api("api/agents/memory/auto-sync"),
      ]);
      if (signal.aborted) return true;
      if (isTransientErr(list) || isTransientErr(changes) || isTransientErr(sync)) return false;
      const entries: MemoryListed[] | null = list?.error ? null : (list?.entries ?? []);
      const st = sync?.error ? undefined : (sync as AutoSyncStatus);
      const last: MemoryChange | undefined = changes?.error ? undefined : changes?.changes?.[0];
      setFig({
        count: entries ? entries.length : null,
        projects: entries ? new Set(entries.flatMap((m) => (m.project ? [m.project.id] : []))).size : null,
        lastChange: last?.at ?? null,
        syncProblem: needsAttention(st),
        // Same rule as the sync panel's own polling: the switch and the Agent disagree, or a run is going.
        watching: !!st && (st.want !== enabled || !st.settled || st.phase === "writing_back" || (st.catchUpPending ?? 0) > 0 || !!st.failing),
      });
      return true;
    },
    [running, enabled],
  );
  // The switch flipping re-reads at once; a run that finishes or fails later is caught by the poll below.
  useRetryLoad(load, [running, reload, view, enabled, tick]);
  useEffect(() => {
    if (!running || !fig?.watching) return;
    const timer = setTimeout(() => setTick((n) => n + 1), POLL_MS);
    return () => clearTimeout(timer);
  }, [running, fig]);

  return (
    <div className="mem-status">
      <div className="mem-status-line">
        <span className={"mem-badge " + (enabled ? "mem-on" : "mem-off")}>
          {tr(enabled ? "mem.status_on" : "mem.status_off")}
        </span>
        {running && fig?.count != null && <span>{tr("mem.status_count", { n: fig.count })}</span>}
        {running && fig?.projects != null && <span>{tr("mem.status_projects", { n: fig.projects })}</span>}
        {running && fig?.lastChange && (
          <span className="muted">{tr("mem.status_last_change", { when: fmtDateTime(fig.lastChange, DATETIME_FULL) })}</span>
        )}
      </div>
      {running && fig?.syncProblem && (
        <p className="mem-warn mem-status-warn">
          {tr("mem.status_sync_problem")}{" "}
          <button type="button" className="ghost" onClick={onOpenSettings}>
            {tr("mem.status_sync_review")}
          </button>
        </p>
      )}
    </div>
  );
}
