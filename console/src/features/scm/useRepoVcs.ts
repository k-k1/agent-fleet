import { useReposStore } from "../repos/store.ts";

/** The working copy's version-control kind from the repos list. A repo the list does not (yet)
 * hold reads as "git": the git panes were the only ones before SVN support, and a layout
 * restored before the first list load must keep rendering them rather than flash an SVN view
 * for a name that turns out to be git. */
export function useRepoVcs(repo: string): "git" | "svn" {
  return useReposStore((s) => (s.repos.find((r) => r.name === repo)?.vcs === "svn" ? "svn" : "git"));
}
