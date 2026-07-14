// CommitDetailView — one commit's detail/diff in its own pane (opened from the
// graph). Port of views/CommitDetailView.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { CommitDetail } from "./GitDiff.tsx";
import type { CommitData, FoldSignal } from "./GitDiff.tsx";

export function CommitDetailView({ repo, path, sha, wrap }: { repo: string; path?: string; sha: string; wrap?: boolean }) {
  const enc = encodeURIComponent(repo || "");
  const [commit, setCommit] = useState<CommitData | null>(null);
  const [localWrap, setLocalWrap] = useState<boolean | null>(null);
  const effWrap = localWrap ?? !!wrap;
  const [fold, setFold] = useState<FoldSignal | undefined>(undefined);
  const foldAll = (open: boolean) => setFold((f) => ({ n: (f?.n ?? 0) + 1, open }));

  useEffect(() => {
    if (!repo || !sha) {
      setCommit(null);
      return;
    }
    let alive = true;
    setCommit(null);
    api(`api/repos/${enc}/show?sha=${encodeURIComponent(sha)}${path ? `&path=${encodeURIComponent(path)}` : ""}`)
      .then((d) => alive && setCommit(d))
      .catch(() => alive && setCommit({ error: true }));
    return () => {
      alive = false;
    };
  }, [enc, sha, repo, path]);

  if (!sha) {
    return (
      <div className="scmview">
        <EmptyState icon="git-commit" title="コミットを選択" hint="グラフでコミットをクリックすると詳細を表示します" />
      </div>
    );
  }
  return (
    <div className="scmview">
      <header className="view-head">
        <span className="view-title" title={repo || ""}>
          <Icon name="git-commit" /> {repo}{path ? ` / ${path}` : ""} · {(sha || "").slice(0, 10)}
        </span>
        <span className="view-spacer" />
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title="全ての diff を開く" onClick={() => foldAll(true)}>
          <Icon name="expand-all" /> <span className="lbl">全て開く</span>
        </button>
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title="全ての diff を閉じる" onClick={() => foldAll(false)}>
          <Icon name="collapse-all" /> <span className="lbl">全て閉じる</span>
        </button>
        <button
          type="button"
          className={"ui-btn ui-btn-ghost ui-btn-sm" + (effWrap ? " on" : "")}
          title="行を折り返す"
          aria-pressed={effWrap}
          onClick={() => setLocalWrap(!effWrap)}
        >
          <Icon name="word-wrap" /> <span className="lbl">折り返し</span>
        </button>
      </header>
      <div className="scm-scroll">
        <CommitDetail commit={commit} wrap={effWrap} fold={fold} />
      </div>
    </div>
  );
}
