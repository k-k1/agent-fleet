// RepoRow — one working-copy row: click the card to open Source Control, 起動 to
// spawn a session (作業を始める modal / quick ▼ / right-click menu with branch
// switch, fast-forward, delete). Presentational: every handler is a prop, so the
// same row renders in the flat Repos list AND as each node's header in the project
// tree (see RepoRowConnected, which wires the handlers from the stores).
// `active` = open in the SCM pane; `selected` = the attached session's repo — both
// just highlight in place (no reordering).
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { copyText } from "../../lib/clipboard.ts";
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

type Integration = NonNullable<Repo["integration"]>;

function integrationLabel(i: Integration): string {
  switch (i.relation) {
    case "same": return "親=";
    case "contained": return "取込済";
    case "unmerged": return `未取込 ${i.worktreeUnique}`;
    case "diverged": return `分岐 ${i.worktreeUnique}↕${i.targetUnique}`;
    case "unknown": return "比較不可";
  }
}

function integrationTitle(i: Integration): string {
  const target = i.targetBranch || "親のHEAD";
  switch (i.relation) {
    case "same":
      return `比較先: ${target}\n親とWTは同じコミットです`;
    case "contained":
      return `比較先: ${target}\nWTのHEADはGit履歴上、親に含まれています（親固有 ${i.targetUnique}コミット）`;
    case "unmerged":
      return `比較先: ${target}\n親に含まれないWT固有コミットが ${i.worktreeUnique} 件あります`;
    case "diverged":
      return `比較先: ${target}\nWT固有 ${i.worktreeUnique} / 親固有 ${i.targetUnique} コミット。マージかリベースが必要です`;
    case "unknown":
      return `比較先: ${target}\nコミット関係を判定できません`;
  }
}

export interface RepoRowProps {
  r: Repo;
  kinds?: string[];
  running?: boolean;
  active?: boolean;
  selected?: boolean;
  /** Session tally shown as a compact badge: green ●n while any are alive, else a
   * muted count of stopped ones. The caller decides the scope (own folder while
   * the node is open; descendants folded in while collapsed). */
  sess?: { alive: number; total: number };
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

export function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, sess, onOpen, onToggle, onOpenFolder, onOpenChanges, onFF, onDelete, onLaunch, onStartWork, onBranchChanged, opens, onFocusPane }: RepoRowProps) {
  const [showLaunch, setShowLaunch] = useState(false);
  const [launchModal, setLaunchModal] = useState(false);
  // Agent kinds only — shell/ssm have no model/prompt, so the modal excludes
  // them; they keep the ▼ quick path.
  const agentKinds = kinds.filter((k) => agentOf(k).caps.chat);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [branchOpen, setBranchOpen] = useState(false);
  const menuRef = useRef<HTMLUListElement>(null);
  const toast = useToast();
  const copyBranch = () => {
    const b = r.branch || "";
    void copyText(b).then((ok) =>
      ok ? toast(`ブランチ名をコピーしました: ${b}`, { kind: "success" }) : toast("コピーに失敗しました"),
    );
  };

  // Context menu: open at the cursor, clamp within the rail, close on outside
  // click / Esc / window blur. Clamped every render (no deps): the JSX re-applies
  // the raw cursor coords as inline style on re-renders, undoing a one-shot clamp.
  useLayoutEffect(() => {
    if (menu && menuRef.current)
      placeFixed(menuRef.current, menu.x, menu.y, wrapRef.current?.closest<HTMLElement>(".app-rail"));
  });
  // The 起動 ▼ quick menu is position:fixed (an absolute dropdown under a row
  // near the rail's foot ran off-screen) — anchor it under the split button,
  // clamped like the context menus.
  const launchMenuRef = useRef<HTMLDivElement>(null);
  useLayoutEffect(() => {
    const el = launchMenuRef.current;
    const anchor = wrapRef.current;
    if (!showLaunch || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 2, wrapRef.current?.closest<HTMLElement>(".app-rail"));
  });
  // Close the launch dropdown on outside click (containment check, so opening
  // another menu closes this one).
  useDismiss([wrapRef, launchMenuRef], showLaunch, () => setShowLaunch(false));
  useDismiss([wrapRef, menuRef], !!menu, () => setMenu(null));
  useMenuRoving(menuRef, !!menu);

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
        data-rail-row=""
        role="treeitem"
        tabIndex={-1}
        title={
          (running ? "クリックで開閉 / Ctrl・中クリックでソース管理を新ペイン\n" : "クリックで開閉\n") +
          (r.path || "") +
          (r.provider ? `\nリモート: ${r.remote || providerLabel(r.provider)}` : "")
        }
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
          {/* One line for every row. Worktrees: the BRANCH is the identity (the
              provider/remote is identical to the parent). Base clones: name, with
              the current branch inline in muted small type — the old second meta
              line (branch + provider) is gone; the provider lives in the tooltip. */}
          <span className="repo-id">
            <span className="repo-name" title={r.worktree ? r.name : undefined}>
              {/* Folder-flavored icons (shared with the ファイル tree's top level):
                  base clone = root-folder, worktree = git-branch. */}
              <Icon name={r.worktree ? "git-branch" : "root-folder"} />
              {r.worktree ? r.branch || r.name : r.name}
            </span>
            {!r.worktree && r.branch && (
              <span className="repo-branch-inline" title={"現在のブランチ: " + r.branch}>
                {r.branch}
              </span>
            )}
          </span>
          {(r.dirty || r.integration || ((r.ahead || r.behind) ?? 0) > 0) && (
            <span className="repo-state">
              {r.dirty && (
                <span className="repo-chip dirty" title="未コミット変更あり">
                  <Icon name="circle-filled" /> 未コミット
                </span>
              )}
              {r.integration && (
                <span
                  className={`repo-chip integration ${r.integration.relation}`}
                  title={integrationTitle(r.integration)}
                >
                  {integrationLabel(r.integration)}
                </span>
              )}
              {/* origin との差分: behind のみ = クリーンに FF 可能 / 両方 = 分岐して
                  いて要マージ / ahead のみ = 未 push。Agent の auto-fetch が origin
                  refs を更新し続けるので、押さなくてもここに出る。 */}
              {((r.ahead || r.behind) ?? 0) > 0 && (
                <span
                  className={"repo-chip ab" + (r.behind ? (r.ahead ? " diverged" : " ff") : "")}
                  title={
                    r.behind
                      ? r.ahead
                        ? `origin と分岐しています（先行 ${r.ahead} / 遅延 ${r.behind}）。fast-forward 不可 — マージかリベースが必要です`
                        : `origin が ${r.behind} コミット先行。クリーンに fast-forward できます（ソース管理 → Fast-Forward）`
                      : `origin より ${r.ahead} コミット先行（未 push）`
                  }
                >
                  {r.ahead ? `↑${r.ahead}` : ""}
                  {r.ahead && r.behind ? " " : ""}
                  {r.behind ? `↓${r.behind}` : ""}
                  {r.behind ? (r.ahead ? " 要マージ" : " FF可") : ""}
                </span>
              )}
            </span>
          )}
          {/* Session tally: alive count (green) wins; otherwise stopped count in
              muted — so a folded project still shows what's running inside. */}
          {sess && sess.alive > 0 && (
            <span className="repo-sess-badge run" title={`稼働中のセッション ${sess.alive} 件`}>
              ●{sess.alive}
            </span>
          )}
          {sess && sess.alive === 0 && sess.total > 0 && (
            <span className="repo-sess-badge" title={`停止中のセッション ${sess.total} 件`}>
              {sess.total}
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
          {/* Hidden until the row is hovered/focused (kept while the quick menu is
              open); touch devices show it always — see repos.css. */}
          <div className={"launch-wrap" + (showLaunch ? " open" : "")} ref={wrapRef} onClick={(e) => e.stopPropagation()}>
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
            {showLaunch &&
              createPortal(
                <div className="ui-menu launch-menu" ref={launchMenuRef} onMouseDown={(e) => e.stopPropagation()}>
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
                </div>,
                document.body,
              )}
          </div>
        </div>
      </div>

      {menu &&
        createPortal(
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
            {r.branch && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); copyBranch(); }}>
                  <Icon name="copy" /> ブランチ名をコピー
                </button>
              </li>
            )}
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
          </ul>,
          document.body,
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
