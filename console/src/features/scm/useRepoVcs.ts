import { useReposStore } from "../repos/store.ts";

/** The working copy's version-control kind from the repos list, or undefined while the list
 * does not (yet) hold the repo. Unknown is NOT git: a layout restored before the first list load
 * would otherwise call the git endpoints and mount the git stage/commit UI for an SVN copy, then
 * swap when the list arrives. A repo that is listed without a `vcs` is git (the field predates
 * SVN support). */
export function useRepoVcs(repo: string): "git" | "svn" | undefined {
  return useReposStore((s) => {
    const r = s.repos.find((x) => x.name === repo);
    return r ? (r.vcs === "svn" ? "svn" : "git") : undefined;
  });
}
