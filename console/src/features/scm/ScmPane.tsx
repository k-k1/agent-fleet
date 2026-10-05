// ScmPane — picks the git or the SVN rendering of the four source-control pane kinds
// (scm / changes / commit / wtdiff). The pane CONTENT stays the same shape for both: for an SVN
// working copy `commitSha` carries the revision number and `scmPath` the Show log path filter,
// so layout persistence, tab identity, titles and badges need no SVN branch of their own.
import { useState } from "react";
import type { ReactNode } from "react";
import type { PaneContent } from "../../layout/types.ts";
import { SourceControlView } from "./SourceControlView.tsx";
import { ChangesView } from "./ChangesView.tsx";
import { CommitDetailView } from "./CommitDetailView.tsx";
import { WorkingDiffView } from "./WorkingDiffView.tsx";
import { SvnLogView } from "./SvnLogView.tsx";
import { SvnChangesView } from "./SvnChangesView.tsx";
import { useRepoVcs } from "./useRepoVcs.ts";
import { useReposStore } from "../repos/store.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useT } from "../../lib/i18n/index.ts";

type ScmContent = Extract<PaneContent, { kind: "scm" | "changes" | "commit" | "wtdiff" }>;

export function ScmPane({ content, wrap, headerActions }: { content: ScmContent; wrap?: boolean; headerActions?: ReactNode }) {
  const tr = useT();
  const known = useRepoVcs(content.scmRepo);
  const refreshRepos = useReposStore((s) => s.refresh);
  // Unknown kind (list not loaded yet, or a restored tab whose repo is gone): ask for the list
  // once and mount nothing until it answers. If the answer still does not hold the repo it is
  // treated as git, which then reports its own "no such repo" as it always did.
  const [settledFor, setSettledFor] = useState("");
  // A transient failure (the workspace agent still booting answers 502) is retried: settling on
  // it would mount the git view for what may be an SVN copy.
  useRetryLoad(async (signal) => {
    if (known !== undefined) return true;
    const ok = await refreshRepos();
    if (signal.aborted) return true;
    if (ok) setSettledFor(content.scmRepo);
    return ok;
  }, [known, content.scmRepo, refreshRepos]);
  if (known === undefined && settledFor !== content.scmRepo) {
    return <div className="scmview"><pre className="diff muted">{tr("scm.loading")}</pre></div>;
  }
  const vcs = known ?? "git";
  switch (content.kind) {
    case "scm":
      return vcs === "svn" ? (
        <SvnLogView key={content.scmRepo + "\0" + (content.scmPath ?? "")} repo={content.scmRepo} path={content.scmPath} headerActions={headerActions} />
      ) : (
        <SourceControlView repo={content.scmRepo} path={content.scmPath} headerActions={headerActions} />
      );
    case "changes":
      return vcs === "svn" ? (
        <SvnChangesView repo={content.scmRepo} headerActions={headerActions} />
      ) : (
        <ChangesView repo={content.scmRepo} headerActions={headerActions} />
      );
    case "commit":
      return <CommitDetailView repo={content.scmRepo} path={content.scmPath} sha={content.commitSha} vcs={vcs} wrap={wrap} headerActions={headerActions} />;
    case "wtdiff":
      return <WorkingDiffView repo={content.scmRepo} path={content.filePath} staged={content.diffStaged} vcs={vcs} wrap={wrap} headerActions={headerActions} />;
  }
}
