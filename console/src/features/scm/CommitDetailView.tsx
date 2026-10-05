// CommitDetailView — one commit's detail/diff in its own pane (opened from the
// graph). Port of views/CommitDetailView.
import { useState } from "react";
import type { ReactNode } from "react";
import { api, errText, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { Icon } from "../../ui/Icon.tsx";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { CommitDetail } from "./GitDiff.tsx";
import type { CommitData, FoldSignal } from "./GitDiff.tsx";

// `vcs` = "svn": `sha` is a revision number and `path` the log's path filter, and the detail
// comes from svn-show (a network call on the server, so it is made once per opened revision).
export function CommitDetailView({ repo, path, sha, vcs = "git", wrap, headerActions }: { repo: string; path?: string; sha: string; vcs?: "git" | "svn"; wrap?: boolean; headerActions?: ReactNode }) {
  const tr = useT();
  const enc = encodeURIComponent(repo || "");
  const [commit, setCommit] = useState<CommitData | null>(null);
  const [localWrap, setLocalWrap] = useState<boolean | null>(null);
  const effWrap = localWrap ?? !!wrap;
  const [fold, setFold] = useState<FoldSignal | undefined>(undefined);
  const foldAll = (open: boolean) => setFold((f) => ({ n: (f?.n ?? 0) + 1, open }));

  // Right after a workspace start the agent is unreachable and api() returns http_5xx, so
  // retry transient failures (isTransientErr) and show only a real agent-side error
  // ({error}) permanently.
  useRetryLoad(async (signal) => {
    if (!repo || !sha) {
      setCommit(null);
      return true;
    }
    setCommit(null);
    let d;
    try {
      const pathQ = path ? `&path=${encodeURIComponent(path)}` : "";
      d = await api(
        vcs === "svn" ? `api/repos/${enc}/svn-show?rev=${encodeURIComponent(sha)}${pathQ}` : `api/repos/${enc}/show?sha=${encodeURIComponent(sha)}${pathQ}`,
      );
    } catch {
      return false; // network drop — retry
    }
    if (signal.aborted) return true;
    if (isTransientErr(d)) return false;
    // An svn-show refusal (no credential, unknown revision) is a terminal answer, not data.
    setCommit(d?.error ? { error: true, message: errText(d.error) } : d);
    return true;
  }, [enc, sha, repo, path, vcs]);

  if (!sha) {
    return (
      <div className="scmview">
        <EmptyState icon="git-commit" title={tr("scm.select_commit")} hint={tr("scm.select_commit_hint")} />
      </div>
    );
  }
  return (
    <div className="scmview">
      {/* The fold/wrap buttons sit inline after a spacer rather than in the
          actions slot: the slot packs its contents at 6px, and these three are
          spaced by the head's own 10px gap today. */}
      <ViewHead actions={headerActions}>
        <span className="view-title" title={repo || ""}>
          <Icon name="git-commit" /> {repo}{path ? ` / ${path}` : ""} · {vcs === "svn" ? `r${sha}` : (sha || "").slice(0, 10)}
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
      </ViewHead>
      <div className="scm-scroll">
        <CommitDetail commit={commit} wrap={effWrap} fold={fold} />
      </div>
    </div>
  );
}
