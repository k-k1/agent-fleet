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
import { fetchConnsWithRetry } from "./connsRetry.ts";
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

// api/connections shells out to every agent's auth check and is expensive (~1.5-2s), and this
// hook is called at the same time by three always-mounted components (ProjectTree /
// OtherSessionsSection / StartHost). A naive fetch would send the same query three times, so
// the in-flight promise is shared at module level and concurrent mounts ride on one request
// (dropped once it resolves — a later mount fetches again as before). Sharing is limited to
// the same key: the key includes connTick (connect/disconnect in Settings) and the workspace's
// running flag, so a disconnected agent cannot linger in the launch menu on a pre-change
// snapshot. Failures collapse to null (the caller's settle contract).
let connsInflight: { key: string; p: Promise<ConnectionsStatus | null> } | null = null;
// Series generation. Once a newer key starts, retries from the old series are pointless and
// stand down as soon as their interval elapses.
let connsSeries = 0;

function connsOnce(): Promise<ConnectionsStatus | null> {
  return api("api/connections")
    .then((d) => (d && !d.error ? (d as ConnectionsStatus) : null))
    .catch(() => null);
}

// Do not keep retrying when the workspace is known to be down — the Agent cannot answer, and
// the effect below refetches with a new key once it is running again. States that are not yet
// known ("…" / "unknown" / "starting") fall on the retrying side: the 502 during boot is
// exactly the case worth retrying.
function wsGone(): boolean {
  const st = useWorkspaceStore.getState().state;
  return st === "stopped" || st === "none";
}

function fetchConns(key: string): Promise<ConnectionsStatus | null> {
  if (connsInflight && connsInflight.key === key) return connsInflight.p;
  const mine = ++connsSeries;
  const p = fetchConnsWithRetry({ once: connsOnce, abort: () => mine !== connsSeries || wsGone() });
  connsInflight = { key, p };
  void p.finally(() => {
    if (connsInflight?.p === p) connsInflight = null;
  });
  return p;
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
  // authenticated agent lights up in the launch menu without a full reload.
  const connTick = useSettingsUI((s) => s.connTick);
  // running is part of the fetch key too, so the answer is refetched the moment the workspace
  // comes up. Right after a start the Agent is not listening yet and the call 502s (connsRetry
  // retries a few times, but a long boot-install outlasts that), so the transition into
  // running is itself the signal that an answer is available now.
  useEffect(() => {
    let alive = true;
    setConnsDone(false); // a refetch re-opens the unknown window
    const settle = (d: ConnectionsStatus | null) => {
      if (!alive) return;
      setConns(d);
      setConnsDone(true);
      setCachedConns(d); // warm the shared cache so leaves (HandoffModal) render instantly
    };
    void fetchConns(`${connTick}:${running ? 1 : 0}`).then(settle);
    return () => {
      alive = false;
    };
  }, [connTick, running]);
  // Gate on a KNOWN-available answer: until the fetch settles, and if it failed (conns
  // null — we cannot prove any agent is usable), the launch pickers stay empty rather
  // than offering agents that would fail on launch. connsSettling lets the pickers say
  // "checking" instead of rendering a bare empty box.
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
