// Repo working-copy folder naming, shared by the New Session / New Repo dialogs.
// A working copy lives at ~/repos/<name>; to clone two branches of the same repo
// side by side they need distinct folder names, so these helpers mirror the
// server (workspace/agent/git.go) and suggest a collision-free name.

// repoNameRe mirrors git.go repoNameRe — the valid folder-name charset. "@" is
// allowed for worktree folders named "<repo>@<branch>"; length 96 to fit them.
export const repoNameRe = /^[A-Za-z0-9][A-Za-z0-9._@-]{0,95}$/;

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

// deriveBranchName turns a free-text first prompt into a short, branch-safe slug used
// as the provisional worktree branch/folder name — so starting work needs no explicit
// branch name (you type WHAT to do; that becomes the name). Leading words, lowercased,
// ASCII word chars only, "-"-joined, length-capped. Returns "" when the prompt yields
// nothing usable (e.g. a Japanese-only prompt) — the caller then falls back to a
// wip-<slug> name, and the LLM branch suggestion can rename it later.
export const deriveBranchName = (prompt: string | null | undefined): string =>
  (prompt || "")
    .toLowerCase()
    .replace(/[^a-z0-9\s-]+/g, " ")
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 6)
    .join("-")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 40);

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
