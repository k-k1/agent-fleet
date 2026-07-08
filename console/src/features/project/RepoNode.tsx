// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder and the folder's file
// subtree. Collapsing a node hides its sessions/files — that's how you focus on one
// working copy ("畳む＝擬似集中"). Node + sub open states persist per folder
// (af-proj-<repo> / -ses / -files). A reveal targeting this folder (a clone just
// landed / フォルダを開く) auto-opens the node and its ファイル sub.
import { useEffect, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import { SessionRow } from "../sessions/SessionRow.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import { RepoRowConnected } from "../repos/RepoRowConnected.tsx";
import type { RepoRailContext } from "../repos/useRepoRail.ts";
import type { Repo } from "../repos/store.ts";
import { useFilesStore } from "../files/store.ts";
import { sessionsInFolder } from "../../lib/project.ts";
import { ProjectFiles } from "./ProjectFiles.tsx";

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
  ctx: RepoRailContext;
  actions: SessionActions;
}

export function RepoNode({ r, ctx, actions }: RepoNodeProps) {
  const sessions = useSessionsStore((s) => s.sessions);
  const reveal = useFilesStore((s) => s.reveal);
  const mine = sessionsInFolder(sessions, r.name);
  const node = usePersistedOpen(`af-proj-${r.name}`);
  const ses = usePersistedOpen(`af-proj-${r.name}-ses`);
  const files = usePersistedOpen(`af-proj-${r.name}-files`);
  const rootPath = "repos/" + r.name;

  // A reveal into this working copy (clone landed here / フォルダを開く) surfaces it:
  // open the node and its ファイル sub so ProjectFiles can expand + select the path.
  const nodeSet = node.set;
  const filesSet = files.set;
  useEffect(() => {
    const p = reveal.path;
    if (!p || (p !== rootPath && !p.startsWith(rootPath + "/"))) return;
    nodeSet(true);
    filesSet(true);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reveal.n]);

  return (
    <li className={"proj-node" + (node.open ? "" : " collapsed")}>
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
          <RepoRowConnected r={r} ctx={ctx} />
        </ul>
      </div>
      {node.open && (
        <div className="proj-node-body">
          <button type="button" className="sess-group-btn proj-sub-btn" onClick={ses.toggle}>
            <Icon name={ses.open ? "chevron-down" : "chevron-right"} />
            <Icon name="terminal" />
            <span className="sess-group-name">セッション</span>
            <span className="sess-group-count">{mine.length}</span>
          </button>
          {ses.open && (
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

          <button type="button" className="sess-group-btn proj-sub-btn" onClick={files.toggle}>
            <Icon name={files.open ? "chevron-down" : "chevron-right"} />
            <Icon name="files" />
            <span className="sess-group-name">ファイル</span>
          </button>
          {/* Lazy: the subtree only fetches once the ファイル sub is open. */}
          {files.open && <ProjectFiles root={rootPath} />}
        </div>
      )}
    </li>
  );
}
