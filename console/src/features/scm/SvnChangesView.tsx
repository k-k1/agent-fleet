// SvnChangesView — read-only local changes of an SVN working copy (issue #1705): the entries of
// `svn status`, each opening its working diff. Local only: this view never reaches the server,
// so it may re-read on the same triggers as the git Changes view. No stage / commit / revert.
import { useCallback, useState } from "react";
import type { ReactNode } from "react";
import { api, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { openFileDiff, openRepoLog } from "./open.ts";
import { changeTone, sortChanges, statusKey } from "./svnLog.ts";
import type { SvnChange } from "./svnLog.ts";

export function SvnChangesView({ repo, headerActions }: { repo: string; headerActions?: ReactNode }) {
  const tr = useT();
  const enc = encodeURIComponent(repo || "");
  const [changes, setChanges] = useState<SvnChange[]>([]);
  const [selPath, setSelPath] = useState("");
  const [err, setErr] = useState("");

  // Resolves whether the load reached a terminal result (a transient gateway failure while the
  // agent boots reports false so useRetryLoad keeps trying).
  const refresh = useCallback(
    async (signal?: AbortSignal) => {
      try {
        const d = await api(`api/repos/${enc}/svn-changes`);
        if (signal?.aborted) return true;
        if (isTransientErr(d)) return false;
        if (d?.error) {
          setErr(String(d.error.message || d.error.code || ""));
          return true;
        }
        setErr("");
        setChanges(sortChanges(d.changes || []));
        return true;
      } catch {
        return false;
      }
    },
    [enc],
  );
  useRetryLoad((signal) => refresh(signal), [refresh]);

  const showDiff = (c: SvnChange) => {
    setSelPath(c.path);
    if (repo) openFileDiff(repo, c.path, false);
  };

  return (
    <div className="scmview">
      <ViewHead
        actions={
          <>
            <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("svn.log")} onClick={() => openRepoLog(repo)}>
              <Icon name="history" />
            </button>
            <button type="button" className="ui-btn ui-btn-ghost ui-iconbtn" title={tr("scm.refresh")} onClick={() => void refresh()}>
              <Icon name="refresh" />
            </button>
            {headerActions}
          </>
        }
      >
        <span className="view-title">
          <Icon name="git-commit" /> {repo} — {tr("scm.changes")}
        </span>
      </ViewHead>
      <div className="changes-body">
        {err && <div className="svnlog-err">{err}</div>}
        <ul className="changes">
          {!err && changes.length === 0 && <EmptyState icon="check" title={tr("scm.no_changes")} />}
          {changes.map((c) => (
            <li key={c.path} className={"change" + (selPath === c.path ? " active" : "")}>
              <span className={"chg " + changeTone(c)} title={tr(statusKey(c.status))}>
                {c.status}
              </span>
              <span className="chg-name" title={c.path} onClick={() => showDiff(c)}>
                {c.path}
              </span>
            </li>
          ))}
        </ul>
        <div className="svnlog-note">{tr("svn.changes_readonly")}</div>
      </div>
    </div>
  );
}
