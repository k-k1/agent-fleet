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
import { compareText } from "./intl.ts";

// The working-copy folder a session runs in. Agent sessions carry `repo`
// (the folder name); fall back to the working dir's basename so a session with a
// dir but no repo field still lands under its folder. "" = no folder (e.g. a
// shell in home) → an orphan.
export function sessionFolder(s: Session): string {
  if (s.repo) return s.repo;
  const dir = s.dir || "";
  return dir ? dir.split("/").filter(Boolean).pop() || "" : "";
}

// groupedRepos partitions working copies into PROJECT GROUPS: each group is a base
// clone followed by its worktrees (parent === base.name), e.g. [base, wt-a, wt-b].
// Bases sort by name; worktrees by CREATION TIME (oldest first) within their base —
// their folder names are temp/<slug> so a name sort is effectively random and reads
// as unstable; chronological order keeps existing worktrees put and appends new ones
// at the end. A worktree missing createdAt (or tying) falls back to name so the order
// stays deterministic. A worktree whose parent is unknown (its base was deleted)
// becomes its own single-member trailing group so it never disappears. The rail
// renders one visual cluster per group so a base and its worktrees read as one
// project, separated from the next.
export function groupedRepos(repos: Repo[]): Repo[][] {
  const byName = (a: Repo, b: Repo) => compareText(a.name, b.name);
  // Worktree order: createdAt ascending (RFC3339 UTC sorts chronologically as a
  // string), then name as a stable tie-break / fallback when a timestamp is absent.
  const byCreated = (a: Repo, b: Repo) => {
    const ca = a.createdAt || "";
    const cb = b.createdAt || "";
    if (ca && cb && ca !== cb) return compareText(ca, cb);
    return byName(a, b);
  };
  const bases = repos.filter((r) => r.worktree !== true).sort(byName);
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
  const groups: Repo[][] = [];
  for (const base of bases) groups.push([base, ...(worktreesByParent.get(base.name) || []).sort(byCreated)]);
  for (const o of orphanWorktrees.sort(byCreated)) groups.push([o]);
  return groups;
}

// orderedRepos is the flat display order (groups concatenated) — each base clone
// followed by its worktrees, then the next base.
export function orderedRepos(repos: Repo[]): Repo[] {
  return groupedRepos(repos).flat();
}

// workingCopyLabel — how a working copy identifies itself where its FOLDER name
// is not the useful handle: as PROJECT + BRANCH. The folder of a worktree is
// "<base>@<slug>" (git.go), which says nothing about the branch it has checked
// out, so a list grouped by folder reads as a pile of slugs; the rail's repo rows
// already solve this by titling a worktree with its branch.
//
// project = the base clone's folder name (a worktree borrows its parent's; an
// orphan worktree whose base is gone falls back to the "<base>@" prefix of its
// own folder). branch = whatever the working copy has checked out, "" when it is
// unknown (SVN copies have none, and a repo the store hasn't loaded yet reports
// nothing) — a caller with no branch should fall back to showing the folder.
export function workingCopyLabel(folder: string, repo?: Repo): { project: string; branch: string } {
  const at = folder.indexOf("@");
  const fromFolder = at > 0 ? folder.slice(0, at) : folder;
  const project = repo?.worktree ? repo.parent || fromFolder : repo?.name || fromFolder;
  return { project, branch: repo?.branch || "" };
}

// worktreeTag — WHICH worktree of the project this working copy is: the "@<slug>" half
// of a worktree folder name ("webshop@checkout-validation" → "checkout-validation").
// "" for a base clone, so a caller can render it as an optional second half after the
// project name (workingCopyLabel's `project`).
//
// The repo entry decides whether this IS a worktree; the folder name only supplies the
// slug. A worktree whose folder carries no "@" (created with a custom directory name)
// falls back to its branch — the folder alone would then say nothing. With no repo entry
// at all (repos not loaded yet, or the folder is gone) the "@" split is all we have.
export function worktreeTag(folder: string, repo?: Repo): string {
  const at = folder.indexOf("@");
  const slug = at > 0 ? folder.slice(at + 1) : "";
  if (!repo) return slug;
  if (!repo.worktree) return "";
  return slug || repo.branch || "";
}

// sessionsInFolder returns the sessions running in one working-copy folder, newest
// first (createdAt desc, matching the old per-dir grouping order).
export function sessionsInFolder(sessions: Session[], folderName: string): Session[] {
  return sessions
    .filter((s) => sessionFolder(s) === folderName)
    .sort((a, b) => compareText(b.createdAt || "", a.createdAt || ""));
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
    .sort((a, b) => compareText(b.createdAt || "", a.createdAt || ""));
}
