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
import { repoPanes, paneCount } from "../../layout/badges.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

export interface RepoRailContext {
  /** Launch kinds gated by the user's set-up connections (all while unknown). */
  launchKinds: string[];
  running: boolean;
  /** The attached session's working copy → highlighted row (null = none). */
  activeRepo: string | null | undefined;
  /** The active SCM/changes/commit pane's repo → highlighted row. */
  scmRepo: string | null | undefined;
  /** repo name → panes showing it (ordinal badges); null when unsplit. */
  rPanes: Map<string, { ordinal: number; id: string }[]> | null;
  multiPane: boolean;
}

export function useRepoRailContext(): RepoRailContext {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const running = useWorkspaceStore((s) => s.state) === "running";

  const [conns, setConns] = useState<ConnectionsStatus | null>(null);
  // Agent connection state gates the 起動 menu. Unknown (null) → show all.
  useEffect(() => {
    let alive = true;
    api("api/connections")
      .then((d) => alive && setConns(d && !d.error ? d : null))
      .catch(() => alive && setConns(null));
    return () => {
      alive = false;
    };
  }, []);
  const ready = (k: string) => !conns || agentOf(k).available({ conns });
  const launchKinds = repoLaunchKinds.filter(ready);

  const multiPane = paneCount(layout) > 1;
  const rPanes = multiPane ? repoPanes(layout) : null;

  // The attached session's repo (from the shared list — no extra fetch).
  const activeSessionName = activePane(layout)?.session ?? null;
  const activeSession = sessions.find((s) => s.name === activeSessionName);
  const activeRepo = activeSession && agentOf(activeSession.kind).caps.runsInDir ? activeSession.repo : null;

  // The SCM pane target → active row.
  const ac = activePane(layout)?.content;
  const scmRepo = ac && (ac.kind === "scm" || ac.kind === "changes" || ac.kind === "commit") ? ac.scmRepo : null;

  return { launchKinds, running, activeRepo, scmRepo, rPanes, multiPane };
}
