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
import { useSettings } from "../../lib/settings.ts";
import { workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { agentOf, repoLaunchKinds } from "../../agents/registry.ts";
import { t, useT } from "../../lib/i18n/index.ts";
import { ordClass } from "../../layout/badges.ts";
import { BranchModal } from "./BranchModal.tsx";
import { ProjectModal } from "./ProjectModal.tsx";
import { ShareCreateModal } from "../sharing/ShareCreateModal.tsx";
import { useMySharesStore } from "../sharing/store.ts";
import { openRepoScm } from "../scm/open.ts";
import { LaunchModal } from "./LaunchModal.tsx";
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import { canFastForwardFromParent, parentSyncLabel } from "./parentSync.ts";
import type { Repo } from "./store.ts";

// Provider display: known SaaS hosts get a friendly label; unknown slugs show as-is.
const PROVIDER_LABEL: Record<string, string> = {
  github: "GitHub",
  bitbucket: "Bitbucket",
  gitlab: "GitLab",
};
const providerLabel = (p: string) => PROVIDER_LABEL[p] || p;

type Integration = NonNullable<Repo["integration"]>;

function integrationTitle(i: Integration): string {
  const target = i.targetBranch || t("repo.parent_head");
  switch (i.relation) {
    case "same":
      return t("repo.sync_title.same", { target });
    case "contained":
      return t("repo.sync_title.contained", { target, n: i.targetUnique });
    case "unmerged":
      return t("repo.sync_title.unmerged", { target, n: i.worktreeUnique });
    case "diverged":
      return t("repo.sync_title.diverged", { target, w: i.worktreeUnique, t: i.targetUnique });
    case "unknown":
      return t("repo.sync_title.unknown", { target });
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
  /** Advances this WT to the parent working copy's HEAD when it is a strict FF. */
  onParentFF?: () => void;
  onDelete?: () => void;
  /** 停止中のセッション全てアーカイブ（右クリックメニュー）。このフォルダ直下の
   * セッションのみが対象 — worktree は別フォルダなので、親レポの一括操作には
   * 含まれない（呼び出し側が sessionsInFolder でスコープ済みの件数/ハンドラを渡す）。 */
  onArchiveStopped?: () => void;
  stoppedCount?: number;
  /** 削除ロック（docs/45）の切替。ロック中は削除メニューが無効になり、空になった
   * worktree の自動 prune も止まる（保護の実体は Agent 側の 403）。 */
  onToggleLock?: (locked: boolean) => void;
  /** SVN (docs/41): update to the latest revision / clear a wedged working-copy lock. */
  onUpdate?: () => void;
  onCleanup?: () => void;
  onLaunch: (kind: string, split: boolean) => void;
  onStartWork: (opts: LaunchOpts) => Promise<LaunchResult>;
  onBranchChanged?: () => void;
  opens?: { ordinal: number; id: string }[];
  onFocusPane?: (id: string) => void;
}

export function RepoRow({ r, kinds = repoLaunchKinds, running = true, active, selected, sess, onOpen, onToggle, onOpenFolder, onOpenChanges, onFF, onParentFF, onDelete, onToggleLock, onUpdate, onCleanup, onLaunch, onStartWork, onBranchChanged, opens, onFocusPane, onArchiveStopped, stoppedCount = 0 }: RepoRowProps) {
  // SVN working copies (docs/41) are flat: no branch/SCM view/worktree, so the card
  // never opens Source Control and the menu shows svn actions (update/cleanup) instead
  // of git ones (branch switch / FF / commit).
  const isSvn = r.vcs === "svn";
  const [showLaunch, setShowLaunch] = useState(false);
  const [launchModal, setLaunchModal] = useState(false);
  // Coding agents only (runsInDir) — shell/ssm have no model/prompt, so the
  // modal excludes them; they keep the ▼ quick path. Not caps.chat: agy is
  // terminal-only (no chat mirror) but still launches through the modal.
  const agentKinds = kinds.filter((k) => agentOf(k).caps.runsInDir);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [branchOpen, setBranchOpen] = useState(false);
  const [projectOpen, setProjectOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const myShares = useMySharesStore((st) => st.shares);
  const isShared = myShares.some((sh) => (sh.scope.type === "repo" || sh.scope.type === "worktree") && sh.scope.key === r.workingCopyId);
  const menuRef = useRef<HTMLUListElement>(null);
  const toast = useToast();
  const tr = useT();
  // 作業グループ (docs/52): base clones toggle membership from the context menu.
  // Worktrees follow their base (no per-worktree assignment), so they get no block.
  const wsets = workingSetList(useSettings());
  const copyBranch = () => {
    const b = r.branch || "";
    void copyText(b).then((ok) =>
      ok ? toast(tr("repo.branch_copied", { branch: b }), { kind: "success" }) : toast(tr("common.copy_failed")),
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
        data-rail-repo={r.name}
        role="treeitem"
        tabIndex={-1}
        title={
          (running ? tr("repo.row_title_running") + "\n" : tr("repo.row_title") + "\n") +
          (r.path || "") +
          (isSvn && r.url ? "\n" + r.url : "") +
          (r.provider ? "\n" + tr("repo.remote_line", { remote: r.remote || providerLabel(r.provider) }) : "")
        }
        // Plain click folds/unfolds the node (SCM is on the right-click menu now).
        // Ctrl/⌘ still opens Source Control in a split (a power gesture) — git only;
        // an svn copy has no SCM view, so it always just folds.
        onClick={(e) => {
          if (running && !isSvn && (e.ctrlKey || e.metaKey)) {
            onOpen(e);
            return;
          }
          onToggle?.();
        }}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => e.button === 1 && running && !isSvn && onOpen(e)}
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
              <span className="repo-branch-inline" title={tr("repo.current_branch", { branch: r.branch })}>
                {r.branch}
              </span>
            )}
            {isSvn && r.revision && (
              <span className="repo-branch-inline" title={tr("repo.revision", { rev: r.revision })}>
                r{r.revision}
              </span>
            )}
            {/* 削除ロック（docs/45）: 鍵バッジ。削除メニューが灰色な理由をここで示す。 */}
            {r.locked && <Icon name="lock" className="repo-lock" title={tr("repo.locked_hint")} />}
            {isShared && <Icon name="broadcast" className="repo-shared" title={tr("repo.shared_badge")} />}
          </span>
          {(r.dirty || r.integration || ((r.ahead || r.behind) ?? 0) > 0) && (
            <span className="repo-state">
              {r.dirty && (
                <span className="repo-chip dirty" title={tr("repo.uncommitted_title")}>
                  <Icon name="circle-filled" /> {tr("repo.uncommitted")}
                </span>
              )}
              {r.integration && (
                <span
                  className={`repo-chip integration ${r.integration.relation}`}
                  title={integrationTitle(r.integration)}
                >
                  {parentSyncLabel(r.integration)}
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
                        ? tr("repo.origin.diverged", { ahead: r.ahead ?? 0, behind: r.behind ?? 0 })
                        : tr("repo.origin.behind", { behind: r.behind ?? 0 })
                      : tr("repo.origin.ahead", { ahead: r.ahead ?? 0 })
                  }
                >
                  {r.ahead ? `↑${r.ahead}` : ""}
                  {r.ahead && r.behind ? " " : ""}
                  {r.behind ? `↓${r.behind}` : ""}
                  {r.behind ? (r.ahead ? tr("repo.need_merge") : tr("repo.ff_ok")) : ""}
                </span>
              )}
            </span>
          )}
          {/* Session tally: alive count (green) wins; otherwise stopped count in
              muted — so a folded project still shows what's running inside. */}
          {sess && sess.alive > 0 && (
            <span className="repo-sess-badge run" title={tr("repo.sess_running", { n: sess.alive })}>
              ●{sess.alive}
            </span>
          )}
          {sess && sess.alive === 0 && sess.total > 0 && (
            <span className="repo-sess-badge" title={tr("repo.sess_stopped", { n: sess.total })}>
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
                  title={tr("common.focus_pane", { ordinal: o.ordinal })}
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
                      ? tr("repo.launch_title")
                      : tr("repo.no_agents")
                    : tr("mirror.ws_stopped")
                }
                disabled={!running || !agentKinds.length}
                onClick={() => setLaunchModal(true)}
              >
                <Icon name="play" /> {tr("launch.launch")}
              </button>
              <button
                type="button"
                className="launch-caret"
                title={running ? tr("repo.quick_launch") : tr("mirror.ws_stopped")}
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
                      title={tr("repo.launch_new_pane")}
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
            {/* Source Control view + git-only ops — hidden for svn (flat working copy). */}
            {!isSvn && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpen(); }}>
                  <Icon name="source-control" /> {tr("repo.open_scm")}
                </button>
              </li>
            )}
            {onOpenFolder && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpenFolder(); }}>
                  <Icon name="folder-opened" /> {tr("repo.open_folder")}
                </button>
              </li>
            )}
            {!isSvn && onOpenChanges && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onOpenChanges(); }}>
                  <Icon name="git-commit" /> {tr("repo.commit_changes")}
                </button>
              </li>
            )}
            {!isSvn && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); setBranchOpen(true); }}>
                  <Icon name="git-branch" /> {tr("repo.switch_branch")}
                </button>
              </li>
            )}
            {!isSvn && r.branch && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); copyBranch(); }}>
                  <Icon name="copy" /> {tr("repo.copy_branch")}
                </button>
              </li>
            )}
            {!isSvn && onFF && !canFastForwardFromParent(r) && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onFF(); }}>
                  <Icon name="arrow-down" /> Fast-Forward
                </button>
              </li>
            )}
            {!isSvn && onParentFF && canFastForwardFromParent(r) && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onParentFF(); }}>
                  <Icon name="arrow-up" /> {tr("repo.ff_parent")}
                </button>
              </li>
            )}
            {/* SVN: update to the latest revision / clear a wedged lock. */}
            {isSvn && onUpdate && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onUpdate(); }}>
                  <Icon name="sync" /> {tr("repo.svn_update")}
                </button>
              </li>
            )}
            {isSvn && onCleanup && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onCleanup(); }}>
                  <Icon name="unlock" /> {tr("repo.svn_cleanup")}
                </button>
              </li>
            )}
            {/* プロジェクトスコープの MCP 設定（docs/56 P0）— 別モーダル（設定モーダルは
                workspace 全体＝user スコープの場所なので混ぜない、docs/57 §3）。 */}
            <li>
              <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); setProjectOpen(true); }}>
                <Icon name="gear" /> {tr("repo.project_settings")}
              </button>
            </li>
            {/* セッション共有(docs/59): read-only/破損等で永続 marker を持てない working
                copy は workingCopyId が無く、repo/worktree 単位の共有対象にできない
                (ShareCreateModal の candidates と同じ制約)。 */}
            {r.workingCopyId && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); setShareOpen(true); }}>
                  <Icon name="broadcast" /> {tr("repo.share")}
                </button>
              </li>
            )}
            <li className="ui-menu-sep" role="separator" />
            {kinds.map((k) => (
              <li key={k}>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onLaunch(k, false); }}>
                  <Icon name={kindIcon(k)} /> {tr("repo.launch_kind", { label: kindLabel(k) })}
                </button>
              </li>
            ))}
            {/* 作業グループ (docs/52): membership toggles — base rows only. */}
            {!r.worktree && wsets.length > 0 && (
              <>
                <li className="ui-menu-sep" role="separator" />
                <li className="ui-menu-caption">{tr("wset.menu_caption")}</li>
                {wsets.map((w) => (
                  <li key={w.id}>
                    <button
                      type="button"
                      className="ui-menu-item"
                      onClick={() => {
                        setMenu(null);
                        toggleWorkingSetMember(w.id, "repos", r.name);
                      }}
                    >
                      <Icon name="check" className={w.repos.includes(r.name) ? "wset-check" : "wset-check off"} /> {w.name}
                    </button>
                  </li>
                ))}
              </>
            )}
            {onArchiveStopped && stoppedCount > 0 && (
              <>
                <li className="ui-menu-sep" role="separator" />
                <li>
                  <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onArchiveStopped(); }}>
                    <Icon name="archive" /> {tr("repo.archive_stopped")}
                    {tr("common.paren", { v: stoppedCount })}
                  </button>
                </li>
              </>
            )}
            {(onDelete || onToggleLock) && <li className="ui-menu-sep" role="separator" />}
            {onToggleLock && (
              <li>
                <button type="button" className="ui-menu-item" onClick={() => { setMenu(null); onToggleLock(!r.locked); }}>
                  <Icon name={r.locked ? "unlock" : "lock"} /> {r.locked ? tr("repo.unlock") : tr("repo.lock")}
                </button>
              </li>
            )}
            {onDelete && (
              <li>
                <button
                  type="button"
                  className="ui-menu-item danger"
                  disabled={!!r.locked}
                  title={r.locked ? tr("repo.locked_hint") : undefined}
                  onClick={() => { setMenu(null); onDelete(); }}
                >
                  <Icon name="trash" /> {tr("repo.delete_wc")}
                </button>
              </li>
            )}
          </ul>,
          document.body,
        )}
      {branchOpen && (
        <BranchModal
          repoName={r.name}
          onClose={() => setBranchOpen(false)}
          onOpenWorktree={(folder) => {
            setBranchOpen(false);
            openRepoScm(folder);
          }}
          onChecked={() => {
            setBranchOpen(false);
            onBranchChanged?.();
          }}
        />
      )}
      {projectOpen && <ProjectModal repo={r.name} onClose={() => setProjectOpen(false)} />}
      {shareOpen && r.workingCopyId && (
        <ShareCreateModal
          initialTarget={`${r.worktree ? "worktree" : "repo"}:${r.workingCopyId}`}
          onClose={() => setShareOpen(false)}
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
          // worktrees are created from the base clone (any base branch). SVN has no
          // worktree at all (docs/41), so it too launches in place only.
          allowWorktree={!r.worktree && !isSvn && !r.unborn}
          isSvn={isSvn}
          isUnborn={!!r.unborn}
          onClose={() => setLaunchModal(false)}
          onLaunch={onStartWork}
        />
      )}
    </li>
  );
}
