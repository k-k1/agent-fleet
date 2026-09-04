// sharedProject — groups the flat recipient-side SharedSession[] into a project/worktree
// tree, mirroring lib/project.ts groupedRepos() (docs/log/59). Unlike the owner side there is no
// separate object for a working copy (Repo[]), so the working copies are synthesised from the
// sessions themselves before the same pairing is applied. Two owners can have working copies of
// the same name, so the grouping runs independently per owner.
import type { SharedSession } from "./store.ts";
import { compareText } from "../../lib/intl.ts";

export interface SharedWorkingCopy {
  workingCopyId: string;
  repo: string;
  worktree: boolean;
  parent: string;
  /** The checked-out branch ("" if unknown). A worktree heading uses this as its name. */
  branch: string;
  sessions: SharedSession[];
}

export interface SharedProjectGroup {
  /** Identity for grouping and persisted keys (the normalised key); ownerEmail is for display. */
  ownerUserKey: string;
  /** The owner's login id (email address). Empty for an identity that has none set. */
  ownerEmail?: string;
  projectName: string;
  copies: SharedWorkingCopy[]; // [base?, ...worktrees] — worktrees only if the base is not shared
}

const byCreatedDesc = (a: SharedSession, b: SharedSession) => compareText(b.createdAt || "", a.createdAt || "");

function copiesOf(sessions: SharedSession[]): SharedWorkingCopy[] {
  const byId = new Map<string, SharedWorkingCopy>();
  for (const s of sessions) {
    const id = s.workingCopyId || `session:${s.name}`; // working copy unknown: one session = one node
    let copy = byId.get(id);
    if (!copy) {
      copy = { workingCopyId: id, repo: s.repo || s.name, worktree: !!s.worktree, parent: s.parent || "",
        branch: s.branch || "", sessions: [] };
      byId.set(id, copy);
    }
    copy.sessions.push(s);
  }
  for (const c of byId.values()) c.sessions.sort(byCreatedDesc);
  return [...byId.values()];
}

// Same algorithm as groupedRepos (lib/project.ts): bases by name, worktrees grouped by
// parent == base.repo and ordered by creation, worktrees with an unknown parent as orphan groups.
function groupedCopies(copies: SharedWorkingCopy[]): SharedWorkingCopy[][] {
  const byName = (a: SharedWorkingCopy, b: SharedWorkingCopy) => compareText(a.repo, b.repo);
  const byCreated = (a: SharedWorkingCopy, b: SharedWorkingCopy) => {
    const ca = a.sessions[0]?.createdAt || "";
    const cb = b.sessions[0]?.createdAt || "";
    if (ca && cb && ca !== cb) return compareText(ca, cb);
    return byName(a, b);
  };
  const bases = copies.filter((c) => !c.worktree).sort(byName);
  const baseNames = new Set(bases.map((c) => c.repo));
  const worktreesByParent = new Map<string, SharedWorkingCopy[]>();
  const orphans: SharedWorkingCopy[] = [];
  for (const c of copies) {
    if (!c.worktree) continue;
    if (c.parent && baseNames.has(c.parent)) {
      const list = worktreesByParent.get(c.parent);
      if (list) list.push(c);
      else worktreesByParent.set(c.parent, [c]);
    } else {
      orphans.push(c);
    }
  }
  const groups: SharedWorkingCopy[][] = [];
  for (const base of bases) groups.push([base, ...(worktreesByParent.get(base.repo) || []).sort(byCreated)]);
  // Worktrees whose parent is not shared are collected into one group per parent name. Unlike the
  // owner side, the recipient often does not get the base working copy at all (a one-worktree-per-
  // session workflow leaves no shared session on the base). One group each would print the same
  // project name as a heading once per worktree.
  const orphansByParent = new Map<string, SharedWorkingCopy[]>();
  const parentless: SharedWorkingCopy[] = [];
  for (const o of orphans) {
    if (!o.parent) { parentless.push(o); continue; }
    const list = orphansByParent.get(o.parent);
    if (list) list.push(o);
    else orphansByParent.set(o.parent, [o]);
  }
  for (const parent of [...orphansByParent.keys()].sort(compareText)) {
    groups.push((orphansByParent.get(parent) || []).sort(byCreated));
  }
  for (const o of parentless.sort(byCreated)) groups.push([o]);
  return groups;
}

export function groupedSharedSessions(sessions: SharedSession[]): SharedProjectGroup[] {
  const byOwner = new Map<string, SharedSession[]>();
  for (const s of sessions) {
    const list = byOwner.get(s.ownerUserKey);
    if (list) list.push(s);
    else byOwner.set(s.ownerUserKey, [s]);
  }
  const out: SharedProjectGroup[] = [];
  for (const owner of [...byOwner.keys()].sort(compareText)) {
    const owned = byOwner.get(owner) || [];
    // Email is per identity, so any row of this owner yields the same value.
    const ownerEmail = owned.find((s) => s.ownerEmail)?.ownerEmail;
    for (const group of groupedCopies(copiesOf(owned))) {
      const head = group[0];
      out.push({ ownerUserKey: owner, ownerEmail, projectName: head.worktree ? head.parent || head.repo : head.repo, copies: group });
    }
  }
  return out;
}
