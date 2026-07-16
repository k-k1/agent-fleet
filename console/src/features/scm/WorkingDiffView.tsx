// WorkingDiffView — ONE working-tree file's diff in its own pane, opened from
// the 変更 view. Port of views/WorkingDiffView.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { Diff } from "./GitDiff.tsx";

export function WorkingDiffView({
  repo,
  path,
  staged,
  wrap,
}: {
  repo: string;
  path: string;
  staged?: boolean | null;
  wrap?: boolean;
}) {
  const tr = useT();
  const [diff, setDiff] = useState("");
  useEffect(() => {
    if (!repo || !path) {
      setDiff("");
      return;
    }
    let alive = true;
    const q = `path=${encodeURIComponent(path)}${staged ? "&staged=1" : ""}`;
    api(`api/repos/${encodeURIComponent(repo)}/diff?${q}`)
      .then((d) => {
        if (!alive) return;
        setDiff(d.diff && d.diff.length ? d.diff : tr("scm.no_diff"));
      })
      .catch(() => {
        if (alive) setDiff(tr("scm.diff_load_failed"));
      });
    return () => {
      alive = false;
    };
  }, [repo, path, staged]);

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title" title={path || ""}>
          <Icon name="git-compare" /> {path || tr("scm.no_file_selected")}
        </span>
        <span className="view-spacer" />
        {staged ? <span className="scm-staged-tag">staged</span> : null}
        {repo && <span className="scm-repo-tag">{repo}</span>}
      </header>
      <div className="scm-scroll">
        <Diff text={diff} wrap={wrap} />
      </div>
    </div>
  );
}
