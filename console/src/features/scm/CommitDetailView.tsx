// CommitDetailView — one commit's detail/diff in its own pane (opened from the
// graph). Port of views/CommitDetailView.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { CommitDetail } from "./GitDiff.tsx";
import type { CommitData, FoldSignal } from "./GitDiff.tsx";

export function CommitDetailView({ repo, path, sha, wrap }: { repo: string; path?: string; sha: string; wrap?: boolean }) {
  const tr = useT();
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
        <EmptyState icon="git-commit" title={tr("scm.select_commit")} hint={tr("scm.select_commit_hint")} />
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
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title={tr("scm.expand_all_diffs")} onClick={() => foldAll(true)}>
          <Icon name="expand-all" /> <span className="lbl">{tr("scm.expand_all")}</span>
        </button>
        <button type="button" className="ui-btn ui-btn-ghost ui-btn-sm" title={tr("scm.collapse_all_diffs")} onClick={() => foldAll(false)}>
          <Icon name="collapse-all" /> <span className="lbl">{tr("scm.collapse_all")}</span>
        </button>
        <button
          type="button"
          className={"ui-btn ui-btn-ghost ui-btn-sm" + (effWrap ? " on" : "")}
          title={tr("scm.word_wrap")}
          aria-pressed={effWrap}
          onClick={() => setLocalWrap(!effWrap)}
        >
          <Icon name="word-wrap" /> <span className="lbl">{tr("scm.wrap")}</span>
        </button>
      </header>
      <div className="scm-scroll">
        <CommitDetail commit={commit} wrap={effWrap} fold={fold} />
      </div>
    </div>
  );
}
