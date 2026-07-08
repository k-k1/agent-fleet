// RepoRow — one working-copy row: click the card to open Source Control, 起動 to
// spawn a session (作業を始める modal / quick ▼ / right-click menu with branch
// switch, fast-forward, delete). Presentational: every handler is a prop, so the
// same row renders in the flat Repos list AND as each node's header in the project
// tree (see RepoRowConnected, which wires the handlers from the stores).
// `active` = open in the SCM pane; `selected` = the attached session's repo — both
// just highlight in place (no reordering).
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { ordClass } from "../../layout/badges.ts";
import { BranchModal } from "./BranchModal.tsx";
import { LaunchModal } from "./LaunchModal.tsx";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import type { Repo } from "./store.ts";

// Provider display: known SaaS hosts get a friendly label; unknown slugs show as-is.
const PROVIDER_LABEL: Record<string, string> = {
  github: "GitHub",
  bitbucket: "Bitbucket",
  gitlab: "GitLab",
};
const providerLabel = (p: string) => PROVIDER_LABEL[p] || p;
const providerIcon = (p: string): string | null => (p === "github" ? "github" : null);

export interface RepoRowProps {
  r: Repo;
  kinds?: string[];
  running?: boolean;
  active?: boolean;
  selected?: boolean;
  onOpen: (e?: RMouseEvent) => void;
  /** Plain click on the card toggles the node's fold (SCM moved to the right-click
   * menu). Ctrl/⌘/middle-click still opens Source Control in a split. */
  onToggle?: () => void;
  onOpenFolder?: () => void;
  onOpenChanges?: () => void;
  onFF?: () => void;
  onDelete?: () => void;
  onLaunch: (kind: string, split: boolean) => void;
  onStartWork: (opts: LaunchOpts) => Promise<LaunchResult>;
  onBranchChanged?: () => void;
  opens?: { ordinal: number; id: string }[];
  onFocusPane?: (id: string) => void;
}

export function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, onOpen, onToggle, onOpenFolder, onOpenChanges, onFF, onDelete, onLaunch, onStartWork, onBranchChanged, opens, onFocusPane }: RepoRowProps) {
  const [showLaunch, setShowLaunch] = useState(false);
  const [launchModal, setLaunchModal] = useState(false);
  // Agent kinds only — shell/ssm have no model/prompt, so the modal excludes
  // them; they keep the ▼ quick path.
  const agentKinds = kinds.filter((k) => agentOf(k).caps.chat);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [branchOpen, setBranchOpen] = useState(false);
  const menuRef = useRef<HTMLUListElement>(null);

  // Context menu: open at the cursor, clamp within the rail, close on outside
  // click / Esc / window blur.
  useLayoutEffect(() => {
    if (menu && menuRef.current)
      placeFixed(menuRef.current, menu.x, menu.y, menuRef.current.closest<HTMLElement>(".app-rail"));
  }, [menu]);
  useEffect(() => {
    if (!menu) return;
    const close = () => setMenu(null);
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && close();
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", onKey);
    window.addEventListener("blur", close);
    return () => {
      document.removeEventListener("mousedown", close);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("blur", close);
    };
  }, [menu]);

  // Close the launch dropdown on outside click (containment check, so opening
  // another menu closes this one).
  useDismiss(wrapRef, showLaunch, () => setShowLaunch(false));

  return (
    <li
      className={"repo-row" + (active || selected ? " active" : "")}
      onContextMenu={(e) => {
        if (!running) return;
        e.preventDefault();
        setMenu({ x: e.clientX, y: e.clientY });
      }}
    >
      <div
        className={"repo-card" + (running ? "" : " disabled")}
        title={(running ? "クリックで開閉 / Ctrl・中クリックでソース管理を新ペイン\n" : "クリックで開閉\n") + (r.path || "")}
        // Plain click folds/unfolds the node (SCM is on the right-click menu now).
        // Ctrl/⌘ still opens Source Control in a split (a power gesture).
        onClick={(e) => {
          if (running && (e.ctrlKey || e.metaKey)) {
            onOpen(e);
            return;
          }
          onToggle?.();
        }}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && running && onOpen(e)}
      >
        <div className="repo-info">
          {/* Worktree rows collapse to one line: the BRANCH is the identity, and the
              provider/remote is identical to the parent — so show the branch here and
              drop the meta line below. Base clones keep name + branch/provider. */}
          <span className="repo-name" title={r.worktree ? r.name : undefined}>
            <Icon name={r.worktree ? "repo-forked" : "repo"} />
            {r.worktree ? r.branch || r.name : r.name}
          </span>
          {(r.dirty || ((r.ahead || r.behind) ?? 0) > 0) && (
            <span className="repo-state">
              {r.dirty && (
                <span className="repo-chip dirty" title="未コミット変更あり">
                  <Icon name="circle-filled" /> 未コミット
                </span>
              )}
              {((r.ahead || r.behind) ?? 0) > 0 && (
                <span className="repo-chip ab" title={`リモートに対して 先行 ${r.ahead ?? 0} / 遅延 ${r.behind ?? 0}`}>
                  {r.ahead ? `↑${r.ahead}` : ""}
                  {r.ahead && r.behind ? " " : ""}
                  {r.behind ? `↓${r.behind}` : ""}
                </span>
              )}
            </span>
          )}
          {opens && opens.length > 0 && (
            <span className="sess-ords repo-ords">
              {opens.map((o) => (
                <button
                  key={o.id}
                  type="button"
                  className={"rail-ord " + ordClass(o.ordinal)}
                  title={`ペイン${o.ordinal}にフォーカス`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onFocusPane?.(o.id);
                  }}
                >
                  {o.ordinal}
                </button>
              ))}
            </span>
          )}
          <div className="launch-wrap" ref={wrapRef} onClick={(e) => e.stopPropagation()}>
            {/* Split button: 起動 opens the modal; the ▼ caret opens the quick
                per-kind dropdown (instant launch, no prompt). */}
            <div className="launch-split">
              <button
                type="button"
                className="launch-main"
                title={
                  running
                    ? agentKinds.length
                      ? "作業を始める（既定は隔離 worktree・エージェント/モデル/最初の指示）"
                      : "利用可能なエージェントがありません"
                    : "ワークスペース停止中"
                }
                disabled={!running || !agentKinds.length}
                onClick={() => setLaunchModal(true)}
              >
                <Icon name="play" /> 起動
              </button>
              <button
                type="button"
                className="launch-caret"
                title={running ? "種別を選んで即起動（プロンプト無し）" : "ワークスペース停止中"}
                disabled={!running}
                onClick={() => setShowLaunch((v) => !v)}
              >
                <Icon name="chevron-down" />
              </button>
            </div>
            {showLaunch && (
              <div className="ui-menu launch-menu">
                {kinds.map((k) => (
                  <button
                    key={k}
                    type="button"
                    className="ui-menu-item"
                    title="Ctrl/中クリックで新ペインに起動"
                    onClick={(e) => {
                      setShowLaunch(false);
                      onLaunch(k, e.ctrlKey || e.metaKey);
                    }}
                    onMouseDown={(e) => e.button === 1 && e.preventDefault()}
                    onAuxClick={(e) => {
                      if (e.button === 1) {
                        e.preventDefault();
                        setShowLaunch(false);
                        onLaunch(k, true);
                      }
                    }}
                  >
                    <Icon name={kindIcon(k)} /> {kindLabel(k)}
                  </button>
                ))}
              </div>
            )}
          </div>
        </div>
        {/* Meta line (base clones only): current branch (left) + git provider (right).
            Worktrees are single-line — their branch is shown as the name above and the
            provider is the same as the parent. */}
        {!r.worktree && (r.branch || r.provider) && (
          <div className="repo-meta">
            {r.branch && (
              <span className="repo-branch" title={"現在のブランチ: " + r.branch}>
                <Icon name="git-branch" />
                <span className="repo-branch-name">{r.branch}</span>
              </span>
            )}
            {r.provider && (
              <span className="repo-provider" title={"リモート: " + (r.remote || r.provider)}>
                {providerIcon(r.provider) && <Icon name={providerIcon(r.provider) as string} />}
                {providerLabel(r.provider)}
              </span>
            )}
          </div>
        )}
      </div>

      {menu && (
        <ul className="ui-menu repo-ctxmenu" ref={menuRef} style={{ left: menu.x, top: menu.y }} role="menu" onMouseDown={(e) => e.stopPropagation()}>
          <li>
            <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpen(); }}>
              <Icon name="source-control" /> ソース管理を開く
            </button>
          </li>
          {onOpenFolder && (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpenFolder(); }}>
                <Icon name="folder-opened" /> フォルダを開く
              </button>
            </li>
          )}
          {onOpenChanges && (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpenChanges(); }}>
                <Icon name="git-commit" /> 変更をコミット
              </button>
            </li>
          )}
          <li>
            <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); setBranchOpen(true); }}>
              <Icon name="git-branch" /> ブランチ切替
            </button>
          </li>
          {onFF && (
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onFF(); }}>
                <Icon name="arrow-down" /> Fast-Forward
              </button>
            </li>
          )}
          <li className="ui-menu-sep" role="separator" />
          {kinds.map((k) => (
            <li key={k}>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onLaunch(k, false); }}>
                <Icon name={kindIcon(k)} /> {kindLabel(k)} を起動
              </button>
            </li>
          ))}
          {onDelete && (
            <>
              <li className="ui-menu-sep" role="separator" />
              <li>
                <button type="button" className="ui-menu-item danger" onClick={() => { setMenu(null); onDelete(); }}>
                  <Icon name="trash" /> ワーキングコピーを削除
                </button>
              </li>
            </>
          )}
        </ul>
      )}
      {branchOpen && (
        <BranchModal
          repoName={r.name}
          onClose={() => setBranchOpen(false)}
          onChecked={() => {
            setBranchOpen(false);
            onBranchChanged?.();
          }}
        />
      )}
      {launchModal && (
        <LaunchModal
          repo={r.name}
          branch={r.branch}
          path={r.path}
          kinds={agentKinds}
          // From a worktree, only in-place launch is offered — spawning a worktree
          // OFF a worktree yields a confusing double-@ name off the wrong base. New
          // worktrees are created from the base clone (any base branch).
          allowWorktree={!r.worktree}
          onClose={() => setLaunchModal(false)}
          onLaunch={onStartWork}
        />
      )}
    </li>
  );
}
