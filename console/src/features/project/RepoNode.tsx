// RepoNode — one working-copy node in the project tree: a collapsible node whose
// header is the repo row (RepoRowConnected: launch / SCM / branch / delete) and
// whose body nests the sessions running in that folder (directly — no
// "セッション" sub-header: it only duplicated the node's own fold) and the
// folder's file subtree. Collapsing a node hides its sessions/files — that's how
// you focus on one working copy ("畳む＝擬似集中"). Node + files open states
// persist per folder (af-proj-<repo> / -files). A reveal targeting this folder
// (a clone just landed / フォルダを開く) auto-opens the node and its ファイル sub.
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

// A small persisted string choice (the ファイル view: tree / changes).
function usePersistedStr(key: string, dflt: string): readonly [string, (v: string) => void] {
  const [v, setV] = useState(() => localStorage.getItem(key) ?? dflt);
  const set = (nv: string) => {
    setV(nv);
    try {
      localStorage.setItem(key, nv);
    } catch {}
  };
  return [v, set] as const;
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
  // Files default COLLAPSED: a repo's whole tree expanded on load buried everything
  // below it. Open on demand (and auto-open on a reveal into this folder).
  const files = usePersistedOpen(`af-proj-${r.name}-files`, false);
  const [filesView, setFilesView] = usePersistedStr(`af-proj-${r.name}-fview`, "tree");
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
          <RepoRowConnected r={r} ctx={ctx} onToggle={node.toggle} />
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

          <div className="proj-sub-head">
            <button type="button" className="sess-group-btn proj-sub-btn" onClick={files.toggle}>
              <Icon name={files.open ? "chevron-down" : "chevron-right"} />
              <Icon name="files" />
              <span className="sess-group-name">ファイル</span>
            </button>
            {/* Tree vs this working copy's git changes. Shown only while the sub is open. */}
            {files.open && (
              <span className="ui-seg sm proj-file-view">
                <button
                  type="button"
                  className={"seg-btn" + (filesView === "tree" ? " active" : "")}
                  title="ツリー"
                  onClick={() => setFilesView("tree")}
                >
                  <Icon name="list-tree" /> ツリー
                </button>
                <button
                  type="button"
                  className={"seg-btn" + (filesView === "changes" ? " active" : "")}
                  title="変更ファイルのみ"
                  onClick={() => setFilesView("changes")}
                >
                  <Icon name="git-compare" /> 変更
                </button>
              </span>
            )}
          </div>
          {/* Lazy: fetch only once the ファイル sub is open. */}
          {files.open && <ProjectFiles root={rootPath} repo={r.name} view={filesView as "tree" | "changes"} />}
        </div>
      )}
    </li>
  );
}
