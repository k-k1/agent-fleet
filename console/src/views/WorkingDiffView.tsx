import { useEffect, useState } from "react";
import { api } from "../api.js";
import Icon from "../components/Icon.jsx";
import { Diff } from "../components/GitDiff.jsx";

// WorkingDiffView shows ONE working-tree file's diff in its own pane — the "diff in a
// separate pane" counterpart to CommitDetailView, opened from the 変更 (changes) view
// via showFileDiff. It just fetches the repo's diff for that path (staged or worktree)
// and renders it; the changes list, staging and commit box stay in the 変更 pane.
export default function WorkingDiffView({
  repo,
  path,
  staged,
  wrap,
}: {
  repo?: string;
  path?: string | null;
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
        <span className="spacer" />
        {staged ? <span className="fi-tag">staged</span> : null}
        {repo && <span className="fi-path muted">{repo}</span>}
      </header>
      <div className="scmbody scm-changes-single">
        <div className="scmleft">
          <Diff text={diff} wrap={wrap} />
        </div>
      </div>
    </div>
  );
}
