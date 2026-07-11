// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder (directly — no
// "セッション" sub-header: it only duplicated the node's own fold) and, for a
// base clone, its worktrees as child nodes. Collapsing a node hides the whole
// project — that's how you focus on one ("畳む＝擬似集中"). The open state
// persists per folder (af-proj-<repo>). File browsing lives in the rail-bottom
// ファイル section (FilesSection), not inside the node.
import { useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import { RepoRowConnected } from "../repos/RepoRowConnected.tsx";
import type { RepoRailContext } from "../repos/useRepoRail.ts";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";
import { sessionsInFolder } from "../../lib/project.ts";
import { useProjectFilter, normQuery, sessionMatches } from "./filter.ts";

// A collapse flag persisted under `key`. While nothing is stored yet the flag
// FOLLOWS `dflt` live (it's derived, not snapshotted) — so a node whose default
// depends on data that loads async (has sessions → open) settles correctly, and
// launching a session into a folded-by-default empty repo pops it open. The
// first explicit toggle pins the choice. Mirrors ui/Section's localStorage
// convention so a folded node stays folded across reloads.
function usePersistedOpen(key: string, dflt = true) {
  const [stored, setStored] = useState<boolean | null>(() => {
    const v = localStorage.getItem(key);
    return v === null ? null : v === "1";
  });
  const open = stored === null ? dflt : stored;
  const set = (v: boolean) => {
    setStored(v);
    try {
      localStorage.setItem(key, v ? "1" : "0");
    } catch {}
  };
  return { open, toggle: () => set(!open), set };
}

interface RepoNodeProps {
  r: Repo;
  /** This base clone's worktrees — rendered as nested child nodes, so folding the
   * base folds the whole project. Absent/empty for worktree nodes themselves. */
  childRepos?: Repo[];
  ctx: RepoRailContext;
  actions: SessionActions;
}

export function RepoNode({ r, childRepos, ctx, actions }: RepoNodeProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const nq = normQuery(useProjectFilter((f) => f.q));
  const mine = sessionsInFolder(sessions, r.name);
  // Empty repos (no sessions anywhere under them, worktrees included) default
  // folded — an unused clone shouldn't take up rail space. The default is live
  // until the user pins a choice (see usePersistedOpen).
  const subtreeTotal =
    mine.length + (childRepos ?? []).reduce((n, c) => n + sessionsInFolder(sessions, c.name).length, 0);
  const node = usePersistedOpen(`af-proj-${r.name}`, subtreeTotal > 0);
  // While filtering, every visible node is forced open (the parent already
  // pruned the tree to matches) and only matching sessions render.
  const open = nq ? true : node.open;
  const shownSessions = nq ? mine.filter((s) => sessionMatches(s, nq)) : mine;
  // Stopped sessions tuck behind a "停止中 n" disclosure (alive ones always
  // show) — but one that's active in a pane must stay visible, and a filter
  // shows its matches directly.
  const alive = shownSessions.filter((s) => s.alive);
  const stopped = shownSessions.filter((s) => !s.alive);
  const [stoppedOpen, setStoppedOpen] = useState(false);
  const showStopped = !!nq || stoppedOpen || stopped.some((s) => s.name === ctx.activeSession);
  // Session tally for the repo row's badge — real counts, not the filtered view:
  // own folder while open (the rows are visible right below); the worktrees'
  // sessions fold in while collapsed, so a folded project still shows what's
  // running inside.
  let sessAlive = mine.filter((s) => s.alive).length;
  let sessTotal = mine.length;
  if (!open && childRepos) {
    for (const c of childRepos) {
      const cs = sessionsInFolder(sessions, c.name);
      sessAlive += cs.filter((s) => s.alive).length;
      sessTotal += cs.length;
    }
  }
  const row = (s: Session) => (
    <SessionRow
      key={s.name}
      s={s}
      selected={ctx.activeSession === s.name}
      opens={ctx.sPanes?.get(s.name) || []}
      multi={ctx.multiPane}
      running={ctx.running}
      actions={actions}
    />
  );
  return (
    <li className={"proj-node" + (open ? "" : " collapsed") + (r.worktree ? " wt" : " base")}>
      <div className="proj-node-head">
        <button
          type="button"
          className="proj-node-caret"
          onClick={node.toggle}
          aria-expanded={open}
          title={open ? "折りたたむ" : "展開"}
        >
          <Icon name={open ? "chevron-down" : "chevron-right"} />
        </button>
        <ul className="sess-list proj-node-repo">
          <RepoRowConnected r={r} ctx={ctx} onToggle={node.toggle} sess={{ alive: sessAlive, total: sessTotal }} />
        </ul>
      </div>
      {open && (
        <div className="proj-node-body">
          {/* Sessions sit directly under the repo row — no sub-header, no empty
              placeholder: a repo with none simply shows nothing here. Alive rows
              always; stopped ones behind the 停止中 disclosure. */}
          {shownSessions.length > 0 && (
            <ul className="sess-list proj-sub-list">
              {alive.map(row)}
              {!nq && stopped.length > 0 && (
                <li className="sess-stopped">
                  <button
                    type="button"
                    className="sess-stopped-btn"
                    onClick={() => setStoppedOpen((v) => !v)}
                    aria-expanded={showStopped}
                    title={showStopped ? "停止中のセッションを隠す" : "停止中のセッションを表示"}
                  >
                    <Icon name={showStopped ? "chevron-down" : "chevron-right"} />
                    <Icon name="debug-pause" /> 停止中
                    <span className="sess-group-count">{stopped.length}</span>
                  </button>
                </li>
              )}
              {showStopped && stopped.map(row)}
            </ul>
          )}

          {/* Worktrees as real child nodes — indentation says "belongs to this
              base" (the old peer-row + group band is gone). */}
          {childRepos && childRepos.length > 0 && (
            <ul className="proj-children">
              {childRepos.map((c) => (
                <RepoNode key={c.name} r={c} ctx={ctx} actions={actions} />
              ))}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}
