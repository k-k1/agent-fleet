// Where "Open shell here" launches for a tree path. The Agent records Meta.Repo from the base
// name of the launch `dir`, so a folder inside a working copy launches in that copy with the
// folder as `subdir`; launching in the folder itself would file the session under "docs"
// (or under an unrelated repo of that name) instead of its working copy.
export function shellLaunchTarget(path: string, repoNames: readonly string[]): { dir: string; subdir: string; inCopy: boolean } {
  const m = path.startsWith("/") ? null : path.match(/^repos\/([^/]+)(?:\/(.*))?$/);
  if (m && repoNames.includes(m[1])) return { dir: `repos/${m[1]}`, subdir: m[2] ?? "", inCopy: true };
  return { dir: path, subdir: "", inCopy: false };
}
