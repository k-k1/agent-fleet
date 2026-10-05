// ScmPane — picks the git or the SVN rendering of the four source-control pane kinds
// (scm / changes / commit / wtdiff). The pane CONTENT stays the same shape for both: for an SVN
// working copy `commitSha` carries the revision number and `scmPath` the Show log path filter,
// so layout persistence, tab identity, titles and badges need no SVN branch of their own.
import type { ReactNode } from "react";
import type { PaneContent } from "../../layout/types.ts";
import { SourceControlView } from "./SourceControlView.tsx";
import { ChangesView } from "./ChangesView.tsx";
import { CommitDetailView } from "./CommitDetailView.tsx";
import { WorkingDiffView } from "./WorkingDiffView.tsx";
import { SvnLogView } from "./SvnLogView.tsx";
import { SvnChangesView } from "./SvnChangesView.tsx";
import { useRepoVcs } from "./useRepoVcs.ts";

type ScmContent = Extract<PaneContent, { kind: "scm" | "changes" | "commit" | "wtdiff" }>;

export function ScmPane({ content, wrap, headerActions }: { content: ScmContent; wrap?: boolean; headerActions?: ReactNode }) {
  const vcs = useRepoVcs(content.scmRepo);
  switch (content.kind) {
    case "scm":
      return vcs === "svn" ? (
        <SvnLogView key={content.scmRepo} repo={content.scmRepo} path={content.scmPath} headerActions={headerActions} />
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
