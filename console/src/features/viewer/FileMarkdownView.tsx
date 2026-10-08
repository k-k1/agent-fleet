// The Markdown preview of a file in the File pane. It is MarkdownView plus the one thing only a
// file knows: which repository its `#N` references belong to (the working copy it lives in).
import { useReposStore } from "../repos/store.ts";
import { originOf } from "../workitems/refs.ts";
import { MarkdownView, type MarkdownViewProps } from "./MarkdownView.tsx";
import { repoOfFilePath } from "./repoOfPath.ts";

export function FileMarkdownView(props: Omit<MarkdownViewProps, "repo" | "workItemRefs"> & { filePath: string }) {
  const { filePath, ...rest } = props;
  const repo = repoOfFilePath(filePath);
  // Ticket links need a recognised remote. Without one (outside ~/repos, no origin, a host that is
  // neither GitHub nor Bitbucket) nothing is linked at all: a qualified `owner/name#N` or a Jira key
  // would otherwise still link, against a repository this file has nothing to do with.
  const linkable = useReposStore((s) => !!repo && originOf(s.repos.find((r) => r.name === repo)) !== null);
  return <MarkdownView {...rest} basePath={filePath} repo={repo} workItemRefs={linkable} />;
}
