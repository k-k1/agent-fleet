// FilesChanges — the ファイル section's 変更 view: every working copy's git
// changes in one list (GET api/fs/changes — cross-repo, each entry carries its
// repo), grouped by working copy. Clicking a row opens the working diff in the
// viewer (same as the SCM pane's changes list); untracked/added files without a
// diff still open it — DiffView falls back sensibly. Revived from the old
// FilesSection (deleted eeded8a), minus its file-management extras.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import FileIcon from "../../ui/FileIcon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useFilesStore } from "../files/store.ts";
import { openFileDiff } from "../scm/open.ts";
import { useT, type MsgKey } from "../../lib/i18n/index.ts";

// A git working-tree change (porcelain XY + repo), as api/fs/changes reports it.
interface FsChange {
  path: string; // home-relative: repos/<repo>/<rel>
  repo: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

// Porcelain XY → a label key + color class (translated at render time).
function changeBadge(c: FsChange): { cls: string; label: MsgKey } {
  if (c.untracked) return { cls: "st-add", label: "pj.st_untracked" };
  const code = c.worktree !== " " && c.worktree !== "" ? c.worktree : c.index;
  if (code === "D") return { cls: "st-del", label: "pj.st_deleted" };
  if (code === "A") return { cls: "st-add", label: "pj.st_added" };
  if (code === "R" || code === "C") return { cls: "st-mod", label: "pj.st_renamed" };
  return { cls: "st-mod", label: "pj.st_modified" };
}

export function FilesChanges() {
  const tr = useT();
  const running = useWorkspaceStore((s) => s.state) === "running";
  const filesTick = useFilesStore((s) => s.tick);
  const [changes, setChanges] = useState<FsChange[] | null>(null);

  useEffect(() => {
    if (!running) return;
    let alive = true;
    setChanges(null);
    api("api/fs/changes")
      .then((d) => alive && setChanges(d.changes || []))
      .catch(() => alive && setChanges([]));
    return () => {
      alive = false;
    };
  }, [running, filesTick]);

  if (!running) return <EmptyState icon="debug-disconnect" title={tr("pj.ws_stopped")} />;
  if (changes === null) return <EmptyState icon="loading" title={tr("pj.loading")} />;
  if (changes.length === 0) return <EmptyState icon="check" title={tr("pj.no_changes")} />;

  const byRepo = changes.reduce((acc: Record<string, FsChange[]>, c) => {
    (acc[c.repo] = acc[c.repo] || []).push(c);
    return acc;
  }, {});

  return (
    <ul className="fstree changeslist" role="list" aria-label={tr("pj.changed_files")}>
      {Object.entries(byRepo).map(([repo, list]) => (
        <li key={repo} className="chg-group">
          <div className="chg-repo">{repo}</div>
          <ul>
            {list.map((c) => {
              const b = changeBadge(c);
              const rel = c.path.startsWith("repos/" + repo + "/") ? c.path.slice(("repos/" + repo + "/").length) : c.path;
              const staged = !c.untracked && c.index !== " " && c.index !== "";
              return (
                <li
                  key={c.path + (c.untracked ? "?" : "")}
                  className="fsrow chg-row"
                  title={c.path + tr("pj.click_open_diff")}
                  onClick={() => openFileDiff(repo, rel, staged)}
                >
                  <span className="fs-file">
                    <span className={"chg-badge " + b.cls}>{tr(b.label)}</span>
                    <span className="fs-ic">
                      <FileIcon name={rel.split("/").pop() || ""} />
                    </span>
                    {rel}
                  </span>
                </li>
              );
            })}
          </ul>
        </li>
      ))}
    </ul>
  );
}
