// WorkingDiffView — ONE working-tree file's diff in its own pane, opened from
// the 変更 view. Port of views/WorkingDiffView.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
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
        setDiff(d.diff && d.diff.length ? d.diff : "(差分なし)");
      })
      .catch(() => {
        if (alive) setDiff("(diff 取得失敗)");
      });
    return () => {
      alive = false;
    };
  }, [repo, path, staged]);

  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title" title={path || ""}>
          <Icon name="git-compare" /> {path || "(ファイル未選択)"}
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
