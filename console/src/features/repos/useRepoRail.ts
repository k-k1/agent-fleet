// Shared rail-level context for repo rows: the derivations every RepoRow needs but
// that would be wasteful to recompute per row — the connection-gated launch kinds,
// which repo the active session / SCM pane points at (for in-place highlight), and
// the pane→ordinal map. Called once by whatever container renders the rows (the
// flat Repos section, or the project tree).
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { repoPanes, sessionPanes, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { useSettingsUI } from "../settings/store.ts";
import { setCachedConns } from "./connsCache.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

export interface RepoRailContext {
  /** Launch kinds the user can actually start: connection-gated, empty until known. */
  launchKinds: string[];
  /** The connection check hasn't settled yet — an empty launchKinds means "checking",
   *  not "nothing available", so pickers can say so instead of showing a bare box. */
  connsSettling: boolean;
  running: boolean;
  /** The attached session's working copy → highlighted row (null = none). */
  activeRepo: string | null | undefined;
  /** The active SCM/changes/commit pane's repo → highlighted row. */
  scmRepo: string | null | undefined;
  /** repo name → panes showing it (ordinal badges); null when unsplit. */
  rPanes: Map<string, { ordinal: number; id: string }[]> | null;
  /** session name → panes showing it (SessionRow ordinal badges); null when unsplit. */
  sPanes: Map<string, { ordinal: number; id: string }[]> | null;
  multiPane: boolean;
  /** The active pane's session name → highlighted SessionRow. */
  activeSession: string | null;
}

export function useRepoRailContext(): RepoRailContext {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const running = useWorkspaceStore((s) => s.state) === "running";

  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  // Whether the answer is in yet. This is NOT derivable from conns alone: a failed or
  // errored fetch also lands on null, and both states used to fall through a `!conns ||`
  // short-circuit that let EVERY kind pass. api/connections really shells out to each
  // agent's auth check (`claude auth status`, agy's token file, …) and takes ~1.5-2s, so
  // that fallback made unauthenticated agents render and stay clickable in the launch
  // pickers for the whole window — long enough to actually launch an unusable session.
  const [connsDone, setConnsDone] = useState(false);
  // connTick bumps after a connect/disconnect in Settings; refetch so a newly
  // authenticated agent lights up in the 起動 menu without a full reload.
  const connTick = useSettingsUI((s) => s.connTick);
  useEffect(() => {
    let alive = true;
    setConnsDone(false); // a refetch re-opens the unknown window
    const settle = (d: ConnectionsStatus | null) => {
      if (!alive) return;
      setConns(d);
      setConnsDone(true);
      setCachedConns(d); // warm the shared cache so leaves (HandoffModal) render instantly
    };
    api("api/connections")
      .then((d) => settle(d && !d.error ? d : null))
      .catch(() => settle(null));
    return () => {
      alive = false;
    };
  }, [connTick]);
  // Gate on a KNOWN-available answer: until the fetch settles, and if it failed (conns
  // null — we cannot prove any agent is usable), the launch pickers stay empty rather
  // than offering agents that would fail on launch. connsSettling lets the pickers say
  // "確認中" instead of rendering a bare empty box.
  const ready = (k: string) => connsDone && !!conns && agentOf(k).available({ conns });
  const launchKinds = repoLaunchKinds.filter(ready);
  const connsSettling = !connsDone;

  const multiPane = paneCount(layout) > 1;
  const rPanes = multiPane ? repoPanes(layout) : null;
  const sPanes = multiPane ? sessionPanes(layout) : null;

  // The attached session's repo (from the shared list — no extra fetch).
  const activeSessionName = activePane(layout)?.session ?? null;
  const activeSessionObj = sessions.find((s) => s.name === activeSessionName);
  const activeRepo = activeSessionObj && agentOf(activeSessionObj.kind).caps.runsInDir ? activeSessionObj.repo : null;

  // The SCM pane target → active row.
  const ac = activePane(layout)?.content;
  const scmRepo = ac && (ac.kind === "scm" || ac.kind === "changes" || ac.kind === "commit") ? ac.scmRepo : null;

  return { launchKinds, connsSettling, running, activeRepo, scmRepo, rPanes, sPanes, multiPane, activeSession: activeSessionName };
}
