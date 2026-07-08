// Project derivation — pure helpers that turn the two flat lists (GET /api/repos,
// GET /api/sessions) into the left rail's working-copy hierarchy. No backend
// change: a "project" is entirely derived here.
//
// A NODE is one working copy (a folder under ~/repos): either a base clone
// (worktree !== true) or a linked worktree (worktree === true, parent = the base
// folder name). Each node carries its own sessions (those running in that folder)
// and its own file subtree (repos/<name>). Base and its worktrees are ordered
// adjacently but NOT visually nested — they're peer nodes distinguished by branch.
import type { Repo } from "../features/repos/store.ts";
import type { Session } from "../types/session.ts";

// The working-copy folder a session runs in. Agent sessions carry `repo`
// (the folder name); fall back to the working dir's basename so a session with a
// dir but no repo field still lands under its folder. "" = no folder (e.g. a
// shell in home) → an orphan.
export function sessionFolder(s: Session): string {
  if (s.repo) return s.repo;
  const dir = s.dir || "";
  return dir ? dir.split("/").filter(Boolean).pop() || "" : "";
}

// orderedRepos lists working copies in display order: each base clone followed by
// its worktrees (parent === base.name), then the next base. Bases and worktrees
// are sorted by name within their group. A worktree whose parent is unknown (its
// base was deleted) becomes its own trailing node so it never disappears.
export function orderedRepos(repos: Repo[]): Repo[] {
  const bases = repos.filter((r) => r.worktree !== true).sort((a, b) => a.name.localeCompare(b.name));
  const baseNames = new Set(bases.map((r) => r.name));
  const worktreesByParent = new Map<string, Repo[]>();
  const orphanWorktrees: Repo[] = [];
  for (const r of repos) {
    if (r.worktree !== true) continue;
    if (r.parent && baseNames.has(r.parent)) {
      const list = worktreesByParent.get(r.parent);
      if (list) list.push(r);
      else worktreesByParent.set(r.parent, [r]);
    } else {
      orphanWorktrees.push(r);
    }
  }
  const byName = (a: Repo, b: Repo) => a.name.localeCompare(b.name);
  const out: Repo[] = [];
  for (const base of bases) {
    out.push(base);
    const wts = worktreesByParent.get(base.name);
    if (wts) out.push(...wts.sort(byName));
  }
  out.push(...orphanWorktrees.sort(byName));
  return out;
}

// sessionsInFolder returns the sessions running in one working-copy folder, newest
// first (createdAt desc, matching the old per-dir grouping order).
export function sessionsInFolder(sessions: Session[], folderName: string): Session[] {
  return sessions
    .filter((s) => sessionFolder(s) === folderName)
    .sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
}

// orphanSessions returns sessions that belong to no known working copy — a folder
// that isn't in the repo list (e.g. a shell in home, or a session whose repo was
// removed). These land in the rail's "その他のセッション" catch-all.
export function orphanSessions(sessions: Session[], repos: Repo[]): Session[] {
  const names = new Set(repos.map((r) => r.name));
  return sessions
    .filter((s) => {
      const f = sessionFolder(s);
      return !f || !names.has(f);
    })
    .sort((a, b) => (b.createdAt || "").localeCompare(a.createdAt || ""));
}
