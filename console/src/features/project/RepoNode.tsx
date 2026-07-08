// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder (and, from P4, its file
// subtree). Collapsing a node hides its sessions/files — that's how you focus on
// one working copy ("畳む＝擬似集中"). Node + sub-section open states persist per
// folder (af-proj-<repo> / af-proj-<repo>-ses).
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
function usePersistedOpen(key: string, dflt = true): readonly [boolean, () => void] {
  const [open, setOpen] = useState(() => {
    const v = localStorage.getItem(key);
    return v === null ? dflt : v === "1";
  });
  const toggle = () =>
    setOpen((o) => {
      const n = !o;
      try {
        localStorage.setItem(key, n ? "1" : "0");
      } catch {}
      return n;
    });
  return [open, toggle] as const;
}

interface RepoNodeProps {
  r: Repo;
  ctx: RepoRailContext;
  actions: SessionActions;
}

export function RepoNode({ r, ctx, actions }: RepoNodeProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const mine = sessionsInFolder(sessions, r.name);
  const [nodeOpen, toggleNode] = usePersistedOpen(`af-proj-${r.name}`);
  const [sesOpen, toggleSes] = usePersistedOpen(`af-proj-${r.name}-ses`);

  return (
    <li className={"proj-node" + (nodeOpen ? "" : " collapsed")}>
      <div className="proj-node-head">
        <button
          type="button"
          className="proj-node-caret"
          onClick={toggleNode}
          aria-expanded={nodeOpen}
          title={nodeOpen ? "折りたたむ" : "展開"}
        >
          <Icon name={nodeOpen ? "chevron-down" : "chevron-right"} />
        </button>
        <ul className="sess-list proj-node-repo">
          <RepoRowConnected r={r} ctx={ctx} />
        </ul>
      </div>
      {nodeOpen && (
        <div className="proj-node-body">
          <button type="button" className="sess-group-btn proj-sub-btn" onClick={toggleSes}>
            <Icon name={sesOpen ? "chevron-down" : "chevron-right"} />
            <Icon name="terminal" />
            <span className="sess-group-name">セッション</span>
            <span className="sess-group-count">{mine.length}</span>
          </button>
          {sesOpen && (
            <ul className="sess-list proj-sub-list">
              {mine.length === 0 ? (
                <li className="proj-sub-empty">セッションなし</li>
              ) : (
                mine.map((s) => (
                  <SessionRow
                    key={s.name}
                    s={s}
                    selected={ctx.activeSession === s.name}
                    opens={ctx.sPanes?.get(s.name) || []}
                    multi={ctx.multiPane}
                    running={ctx.running}
                    actions={actions}
                  />
                ))
              )}
            </ul>
          )}
        </div>
      )}
    </li>
  );
}
