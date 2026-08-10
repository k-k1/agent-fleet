// FilesChanges — the ファイル section's 変更 view: every working copy's git
// changes in one list (GET api/fs/changes — cross-repo, each entry carries its
// repo), grouped by working copy and headed by プロジェクト + ブランチ
// (WorkingCopyLabel) rather than the "<base>@<slug>" folder the API groups by:
// the folder is the identity, the project+branch is what a reader recognises.
// Groups follow the rail's project order (base, then its worktrees oldest-first)
// so the two lists read the same way. Clicking a row opens the working diff in the
// viewer (same as the SCM pane's changes list) — EXCEPT an untracked file, which
// git has no diff for: the working diff would be an empty pane, so the row opens
// the file itself in the viewer instead. The row menu still offers 差分 for anyone
// who wants it. Revived from the old FilesSection (deleted 52582b9), minus its
// file-management extras.
import { useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useFilesStore } from "../files/store.ts";
import { useReposStore } from "../repos/store.ts";
import { orderedRepos } from "../../lib/project.ts";
import { useActiveWorkingSet, folderBase } from "../../lib/workingSetsStore.ts";
import { compareText } from "../../lib/intl.ts";
import { WorkingCopyLabel } from "./WorkingCopyLabel.tsx";
import { openFileDiff } from "../scm/open.ts";
import { openFileMode } from "../viewer/openFile.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

// A git working-tree change (porcelain XY + repo), as api/fs/changes reports it.
interface FsChange {
  path: string; // home-relative: repos/<repo>/<rel>
  repo: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

// The porcelain code that decides the row's badge: the worktree column when it
// says anything, else the index column.
const changeCode = (c: FsChange): string | undefined =>
  c.worktree !== " " && c.worktree !== "" ? c.worktree : c.index;

// Porcelain XY → a label key + color class (translated at render time).
function changeBadge(c: FsChange): { cls: string; label: MsgKey } {
  if (c.untracked) return { cls: "st-add", label: "pj.st_untracked" };
  const code = changeCode(c);
  if (code === "D") return { cls: "st-del", label: "pj.st_deleted" };
  if (code === "A") return { cls: "st-add", label: "pj.st_added" };
  if (code === "R" || code === "C") return { cls: "st-mod", label: "pj.st_renamed" };
  return { cls: "st-mod", label: "pj.st_modified" };
}

// One row's menu target. `rel` and `staged` are what the diff needs, `path` (the
// home-relative one the API reports) what the file view needs, and `deleted`
// gates 表示 / 編集 — a deleted file has no content left to open.
interface ChangeMenu {
  x: number;
  y: number;
  /** The ⋯ button the menu hangs under; null when opened at the cursor. */
  anchor: HTMLElement | null;
  repo: string;
  rel: string;
  path: string;
  staged: boolean;
  deleted: boolean;
}

export function FilesChanges() {
  const tr = useT();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const filesTick = useFilesStore((s) => s.tick);
  const repos = useReposStore((s) => s.repos);
  const wset = useActiveWorkingSet();
  const [changes, setChanges] = useState<FsChange[] | null>(null);
  const [menu, setMenu] = useState<ChangeMenu | null>(null);
  const listRef = useRef<HTMLUListElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const anchorRef = useRef<HTMLElement | null>(null);
  anchorRef.current = menu?.anchor ?? null;

  // Esc / outside-click close, ↑↓ roving, and role=menu — the same shared trio
  // the session and repo row menus use.
  useDismiss([menuRef, anchorRef], !!menu, () => setMenu(null));
  useMenuRoving(menuRef, !!menu);
  // Clamp EVERY render, before paint: the JSX re-applies the raw coordinates as
  // inline style on each re-render, which would undo a one-shot clamp for a menu
  // opened near the rail's foot (same reasoning as the file tree's menu).
  useLayoutEffect(() => {
    const el = menuRef.current;
    if (!menu || !el) return;
    const bounds = listRef.current?.closest<HTMLElement>(".app-rail");
    if (menu.anchor) {
      const a = menu.anchor.getBoundingClientRect();
      placeFixed(el, a.right - el.offsetWidth, a.bottom + 2, bounds);
    } else placeFixed(el, menu.x, menu.y, bounds);
  });
  const runMenu = (fn: () => void) => {
    setMenu(null);
    fn();
  };

  // WS 起動直後は agent 不通で api() が http_5xx を返すので過渡的失敗は再試行（isTransientErr）。
  useRetryLoad(async (signal) => {
    if (!running) return true; // nothing to load while the WS is stopped
    setChanges(null);
    let d;
    try {
      d = await api("api/fs/changes");
    } catch {
      return false; // network drop — retry
    }
    if (signal.aborted) return true;
    if (isTransientErr(d)) return false;
    setChanges(d.changes || []);
    return true;
  }, [running, filesTick]);

  if (!running) return <EmptyState icon="debug-disconnect" title={tr("pj.ws_stopped")} />;
  if (changes === null) return <EmptyState icon="loading" title={tr("pj.loading")} />;
  // 作業グループ (docs/52): keep only changes in the group's working copies —
  // a worktree folder ("<base>@<slug>") resolves via its base prefix.
  const scoped = wset ? changes.filter((c) => wset.repos.includes(folderBase(c.repo))) : changes;
  if (scoped.length === 0) return <EmptyState icon="check" title={tr("pj.no_changes")} />;

  const byRepo = scoped.reduce((acc: Record<string, FsChange[]>, c) => {
    (acc[c.repo] = acc[c.repo] || []).push(c);
    return acc;
  }, {});
  // Group ORDER follows the rail (base clone, then its worktrees oldest-first);
  // the API answers in ~directory order, which scatters a project's worktrees.
  // A folder the repos store does not know sorts last, by name — MAX_SAFE_INTEGER
  // subtracts to 0 between two of them, so the name tie-break decides.
  const rank = new Map(orderedRepos(repos).map((r, i) => [r.name, i]));
  const groups = Object.entries(byRepo).sort(
    ([a], [b]) =>
      (rank.get(a) ?? Number.MAX_SAFE_INTEGER) - (rank.get(b) ?? Number.MAX_SAFE_INTEGER) || compareText(a, b),
  );

  return (
    <>
      <ul ref={listRef} className="fstree changeslist" role="list" aria-label={tr("pj.changed_files")}>
        {groups.map(([repo, list]) => (
          <li key={repo} className="chg-group">
            <div className="chg-repo" title={repo}>
              <WorkingCopyLabel folder={repo} />
            </div>
            <ul>
              {list.map((c) => {
                const b = changeBadge(c);
                const rel = c.path.startsWith("repos/" + repo + "/") ? c.path.slice(("repos/" + repo + "/").length) : c.path;
                const staged = !c.untracked && c.index !== " " && c.index !== "";
                const target = (e: { clientX: number; clientY: number }, anchor: HTMLElement | null): ChangeMenu => ({
                  x: e.clientX,
                  y: e.clientY,
                  anchor,
                  repo,
                  rel,
                  path: c.path,
                  staged,
                  deleted: !c.untracked && changeCode(c) === "D",
                });
                return (
                  <li
                    key={c.path + (c.untracked ? "?" : "")}
                    className="fsrow chg-row"
                    title={c.path + tr(c.untracked ? "pj.click_open_file" : "pj.click_open_diff")}
                    onClick={() => (c.untracked ? openFileMode(c.path, "view") : openFileDiff(repo, rel, staged))}
                    onContextMenu={(e) => {
                      e.preventDefault();
                      e.stopPropagation();
                      setMenu(target(e, null));
                    }}
                  >
                    <span className="fs-file">
                      <span className={"chg-badge " + b.cls}>{tr(b.label)}</span>
                      <span className="fs-ic">
                        <FileIcon name={rel.split("/").pop() || ""} />
                      </span>
                      {rel}
                    </span>
                    {/* ⋯ — the same menu the right-click opens, for pointers that
                        have no right button and for keyboard reach. */}
                    <button
                      type="button"
                      className="chg-menu-btn"
                      title={tr("pj.row_menu")}
                      aria-haspopup="menu"
                      onClick={(e) => {
                        e.preventDefault();
                        e.stopPropagation(); // the row itself opens the diff
                        const btn = e.currentTarget;
                        setMenu((m) => (m && m.anchor === btn ? null : target(e, btn)));
                      }}
                    >
                      <Icon name="ellipsis" />
                    </button>
                  </li>
                );
              })}
            </ul>
          </li>
        ))}
      </ul>
      {menu &&
        createPortal(
          <ul
            className="ui-menu chg-ctxmenu"
            ref={menuRef}
            style={{ left: menu.x, top: menu.y }}
            role="menu"
            onMouseDown={(e) => e.stopPropagation()}
          >
            <li>
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => runMenu(() => openFileDiff(menu.repo, menu.rel, menu.staged))}
              >
                <Icon name="git-compare" /> {tr("view.diff")}
              </button>
            </li>
            {/* 表示 / 編集 open the file itself, so a deleted one has nothing to
                show — the entries stay listed but disabled, with the reason on
                the tooltip. */}
            <li>
              <button
                type="button"
                className="ui-menu-item"
                disabled={menu.deleted}
                title={menu.deleted ? tr("pj.deleted_no_open") : undefined}
                onClick={() => runMenu(() => openFileMode(menu.path, "view"))}
              >
                <Icon name="eye" /> {tr("editor.mode.view")}
              </button>
            </li>
            <li>
              <button
                type="button"
                className="ui-menu-item"
                disabled={menu.deleted}
                title={menu.deleted ? tr("pj.deleted_no_open") : undefined}
                onClick={() => runMenu(() => openFileMode(menu.path, "edit"))}
              >
                <Icon name="edit" /> {tr("editor.mode.edit")}
              </button>
            </li>
          </ul>,
          document.body,
        )}
    </>
  );
}
