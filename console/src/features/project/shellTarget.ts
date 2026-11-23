// Where "Open shell here" launches for a tree path. The Agent records Meta.Repo from the base
// name of the launch `dir`, so a folder inside a working copy launches in that copy with the
// folder as `subdir`; launching in the folder itself would file the session under "docs"
// (or under an unrelated repo of that name) instead of its working copy.
export function shellLaunchTarget(path: string, repoNames: readonly string[]): { dir: string; subdir: string; inCopy: boolean } {
  const m = path.startsWith("/") ? null : path.match(/^repos\/([^/]+)(?:\/(.*))?$/);
  // CleanSubdir (Agent) rejects a leading "~" as a home expansion, so a folder literally named
  // "~backup" has to be spelled "./~backup"; the Agent normalizes the "./" away.
  // A folder inside a space launches in the space's root with the folder as `subdir`: the Agent
  // recognises a space only by the directory directly under ~/spaces (spacex.FolderOf), so a shell
  // started in `spaces/<space>/notes` itself would belong to no space (or to a repository called
  // "notes"). Not "inCopy": a space shell is repo-less and joins the working set like one.
  const sp = path.startsWith("/") ? null : path.match(/^spaces\/([^/]+)(?:\/(.*))?$/);
  if (sp) return { dir: `spaces/${sp[1]}`, subdir: sp[2]?.startsWith("~") ? `./${sp[2]}` : (sp[2] ?? ""), inCopy: false };
  if (m && repoNames.includes(m[1])) return { dir: `repos/${m[1]}`, subdir: m[2]?.startsWith("~") ? `./${m[2]}` : (m[2] ?? ""), inCopy: true };
  return { dir: path, subdir: "", inCopy: false };
}

/** Whether the answer depends on the working-copy list: only a path under repos/ can belong to one. */
export const needsRepoList = (path: string) => !path.startsWith("/") && path.startsWith("repos/");
