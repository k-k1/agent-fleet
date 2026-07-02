// Repo working-copy folder naming, shared by the New Session / New Repo dialogs.
// A working copy lives at ~/repos/<name>; to clone two branches of the same repo
// side by side they need distinct folder names, so these helpers mirror the
// server (workspace/agent/git.go) and suggest a collision-free name.

// repoNameRe mirrors git.go repoNameRe — the valid folder-name charset.
export const repoNameRe = /^[A-Za-z0-9][A-Za-z0-9._-]{0,59}$/;

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

// uniqueRepoName returns base, or base-2 … when a working copy of that folder name
// already exists, so a second clone lands in its own directory.
export const uniqueRepoName = (base: string, taken: Set<string>): string => {
  if (!taken.has(base)) return base;
  for (let i = 2; i < 1000; i++) {
    const n = `${base}-${i}`.slice(0, 60);
    if (!taken.has(n)) return n;
  }
  return base;
};
