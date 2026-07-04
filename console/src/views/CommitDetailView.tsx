import { useEffect, useState } from "react";
import { useApp } from "../state.jsx";
import { api } from "../api.js";
import Icon from "../components/Icon.jsx";
import { CommitDetail } from "../components/GitDiff.jsx";
import type { CommitData } from "../components/GitDiff.jsx";
import EmptyState from "../components/EmptyState.jsx";

// CommitDetailView is one commit's detail/diff in its own pane, opened by clicking a
// commit in the graph. Fetches GET /repos/{name}/show?sha= and renders the shared
// CommitDetail (header + colored patch).
export default function CommitDetailView({
  repo,
  sha,
  paneId,
  wrap,
}: {
  repo?: string;
  sha?: string;
  paneId?: string;
  wrap?: boolean;
}) {
  const { scmRepo: ctxRepo, commitSha: ctxSha, closePane } = useApp();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
  const target = sha !== undefined ? sha : ctxSha || undefined;
  const enc = encodeURIComponent(scmRepo || "");
  const [commit, setCommit] = useState<CommitData | null>(null);

  useEffect(() => {
    if (!scmRepo || !target) {
      setCommit(null);
      return;
    }
    let alive = true;
    setCommit(null);
    api(`api/repos/${enc}/show?sha=${encodeURIComponent(target)}`)
      .then((d) => alive && setCommit(d))
      .catch(() => alive && setCommit({ error: true }));
    return () => {
      alive = false;
    };
  }, [enc, target, scmRepo]);

  if (!target) {
    return (
      <div className="scmview">
        <div className="cd-empty">
          <EmptyState icon="git-commit" message="コミットを選択" hint="グラフでコミットをクリックすると詳細を表示します" />
        </div>
      </div>
    );
  }
  return (
    <div className="scmview commit-view">
      <header className="view-head">
        <span className="view-title"><Icon name="git-commit" /> {(target || "").slice(0, 10)}</span>
        <span className="spacer" />
        <button
          className="ghost scm-act"
          title="diff を閉じる"
          onClick={() => paneId && closePane(paneId)}
        >
          <Icon name="close" /> <span className="lbl">閉じる</span>
        </button>
      </header>
      <div className="commit-view-body">
        <CommitDetail commit={commit} wrap={wrap} />
      </div>
    </div>
  );
}
