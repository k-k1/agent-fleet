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

// api/connections は各エージェントの認証チェックへシェルアウトする重い呼び出し（~1.5-2s）で、
// このフックは常駐 3 コンポーネント（ProjectTree / OtherSessionsSection / StartHost）から
// 同時に呼ばれる。素朴に fetch すると同じ問い合わせが 3 重に飛ぶので、in-flight の Promise を
// モジュールレベルで共有して同時マウント分を 1 本に相乗りさせる（解決後は捨てる — 後からの
// マウントは従来どおり取り直す）。相乗りは同一キーに限る: キーは connTick（設定での接続/解除）
// と WS の running を含むので、変更前のスナップショットで確定して解除済みエージェントが起動
// メニューに残り続けることはない。失敗は null に畳む（呼び手の settle 契約）。
let connsInflight: { key: string; p: Promise<ConnectionsStatus | null> } | null = null;
// 系列の世代。新しいキーで走り出したら古い系列の取り直しは用済み（間隔待ちが明けた時点で降りる）。
let connsSeries = 0;

function connsOnce(): Promise<ConnectionsStatus | null> {
  return api("api/connections")
    .then((d) => (d && !d.error ? (d as ConnectionsStatus) : null))
    .catch(() => null);
}

// WS が居ないと分かっているなら粘らない — Agent が応答しないのは当然で、running へ戻った
// ときに下の effect が新しいキーで取り直す。まだ分からない状態（"…" / "unknown" / "starting"）
// は粘る側に倒す（起動途中の 502 こそが取り直したいケース）。
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
  // authenticated agent lights up in the 起動 menu without a full reload.
  const connTick = useSettingsUI((s) => s.connTick);
  // running も取得のキーに入れる: WS が上がった瞬間に取り直す。起動直後は Agent がまだ
  // listen しておらず 502 になるので（connsRetry が数回粘るが、boot-install の長い起動は
  // それでも足りない）、running への遷移そのものを「今なら答えが返る」合図として使う。
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
