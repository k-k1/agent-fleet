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
  wrap,
}: {
  repo?: string;
  sha?: string;
  wrap?: boolean;
}) {
  const { scmRepo: ctxRepo, commitSha: ctxSha } = useApp();
  const scmRepo = repo !== undefined ? repo : ctxRepo;
  const target = sha !== undefined ? sha : ctxSha || undefined;
  const enc = encodeURIComponent(scmRepo || "");
  const [commit, setCommit] = useState<CommitData | null>(null);
  const [localWrap, setLocalWrap] = useState<boolean | null>(null); // per-view soft-wrap override
  const effWrap = localWrap ?? !!wrap;

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
        <span className="view-title" title={scmRepo || ""}>
          <Icon name="git-commit" /> {scmRepo} · {(target || "").slice(0, 10)}
        </span>
        <span className="spacer" />
        <button
          className={"ghost scm-act" + (effWrap ? " on" : "")}
          title="行を折り返す"
          aria-pressed={effWrap}
          onClick={() => setLocalWrap(!effWrap)}
        >
          <Icon name="word-wrap" /> <span className="lbl">折り返し</span>
        </button>
      </header>
      <div className="commit-view-body">
        <CommitDetail commit={commit} wrap={effWrap} />
      </div>
    </div>
  );
}
