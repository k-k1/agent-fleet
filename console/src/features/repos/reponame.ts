// Repo working-copy folder naming, shared by the New Session / New Repo dialogs.
// A working copy lives at ~/repos/<name>; to clone two branches of the same repo
// side by side they need distinct folder names, so these helpers mirror the
// server (workspace/agent/git.go) and suggest a collision-free name.

// repoNameRe mirrors git.go repoNameRe — the valid folder-name charset. Unicode
// letters/numbers (\p{L}\p{N}, hence the /u flag) so a folder can be named in
// Japanese (e.g. an SVN checkout target); "@" is allowed for worktree folders named
// "<repo>@<branch>"; length 96 to fit them. First char must be a letter/number and
// "/" is excluded, so traversal ("..", "/") stays impossible.
export const repoNameRe = /^[\p{L}\p{N}][\p{L}\p{N}._@-]{0,95}$/u;

// deriveRepoName mirrors git.go deriveRepoName: last path segment of a clone URL
// minus a trailing ".git" — the default working-copy folder name.
export const deriveRepoName = (remote: string | null | undefined): string => {
  const s = (remote || "").trim().replace(/\/+$/, "").replace(/\.git$/, "");
  const i = Math.max(s.lastIndexOf("/"), s.lastIndexOf(":"));
  return i >= 0 ? s.slice(i + 1) : s;
};

// sanitizeSeg makes a branch usable as part of a folder name (repoNameRe charset).
export const sanitizeSeg = (s: string | null | undefined): string =>
  (s || "").replace(/[^A-Za-z0-9._-]/g, "-").replace(/^-+/, "").slice(0, 59) || "branch";

// sanitizeFolderName coerces an arbitrary string into a valid repoNameRe folder
// name while PRESERVING Unicode letters/numbers — so a Japanese SVN path segment
// survives as a suggested checkout folder name instead of being flattened to
// dashes. Non-charset runes become "-", any leading "." "-" "@" (which repoNameRe
// forbids as the first char) is stripped, and the result is capped to 96 runes.
export const sanitizeFolderName = (s: string | null | undefined): string =>
  [...(s || "").replace(/[^\p{L}\p{N}._@-]/gu, "-").replace(/^[.@-]+/u, "")].slice(0, 96).join("");

// uniqueRepoName returns base, or base-2 … when a working copy of that folder name
// already exists, so a second clone lands in its own directory.
export const uniqueRepoName = (base: string, taken: Set<string>): string => {
  if (!taken.has(base)) return base;
  for (let i = 2; i < 1000; i++) {
    const n = `${base}-${i}`.slice(0, 96);
    if (!taken.has(n)) return n;
  }
  return base;
};
