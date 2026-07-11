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
import { sessionsInFolder } from "../../lib/project.ts";

// A collapse flag persisted under `key` (default open). Mirrors ui/Section's
// localStorage convention so a folded node/sub stays folded across reloads.
function usePersistedOpen(key: string, dflt = true) {
  const [open, setOpenState] = useState(() => {
    const v = localStorage.getItem(key);
    return v === null ? dflt : v === "1";
  });
  const set = (v: boolean) => {
    setOpenState(v);
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
  const mine = sessionsInFolder(sessions, r.name);
  const node = usePersistedOpen(`af-proj-${r.name}`);
  // Session tally for the repo row's badge: own folder while open (the rows are
  // visible right below); the worktrees' sessions fold in while collapsed, so a
  // folded project still shows what's running inside.
  let sessAlive = mine.filter((s) => s.alive).length;
  let sessTotal = mine.length;
  if (!node.open && childRepos) {
    for (const c of childRepos) {
      const cs = sessionsInFolder(sessions, c.name);
      sessAlive += cs.filter((s) => s.alive).length;
      sessTotal += cs.length;
    }
  }
  return (
    <li className={"proj-node" + (node.open ? "" : " collapsed") + (r.worktree ? " wt" : " base")}>
      <div className="proj-node-head">
        <button
          type="button"
          className="proj-node-caret"
          onClick={node.toggle}
          aria-expanded={node.open}
          title={node.open ? "折りたたむ" : "展開"}
        >
          <Icon name={node.open ? "chevron-down" : "chevron-right"} />
        </button>
        <ul className="sess-list proj-node-repo">
          <RepoRowConnected r={r} ctx={ctx} onToggle={node.toggle} sess={{ alive: sessAlive, total: sessTotal }} />
        </ul>
      </div>
      {node.open && (
        <div className="proj-node-body">
          {/* Sessions sit directly under the repo row — no sub-header, no empty
              placeholder: a repo with none simply shows nothing here. */}
          {mine.length > 0 && (
            <ul className="sess-list proj-sub-list">
              {mine.map((s) => (
                <SessionRow
                  key={s.name}
                  s={s}
                  selected={ctx.activeSession === s.name}
                  opens={ctx.sPanes?.get(s.name) || []}
                  multi={ctx.multiPane}
                  running={ctx.running}
                  actions={actions}
                />
              ))}
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
