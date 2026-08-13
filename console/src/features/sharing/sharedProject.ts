// sharedProject — groups the flat 受信側 SharedSession[] into a project/worktree
// tree, mirroring lib/project.ts groupedRepos() (docs/59)。所有者側と違い working
// copy を表す別オブジェクト(Repo[])が無いので、セッション自身から working copy を
// 合成してから同じペアリングをかける。異なる owner が同名の working copy を持つ
// ことがあるため、グルーピングは owner ごとに独立して行う。
import type { SharedSession } from "./store.ts";
import { compareText } from "../../lib/intl.ts";

export interface SharedWorkingCopy {
  workingCopyId: string;
  repo: string;
  worktree: boolean;
  parent: string;
  sessions: SharedSession[];
}

export interface SharedProjectGroup {
  ownerUserKey: string;
  projectName: string;
  copies: SharedWorkingCopy[]; // [base?, ...worktrees] — base が共有されていなければ worktree のみ
}

const byCreatedDesc = (a: SharedSession, b: SharedSession) => compareText(b.createdAt || "", a.createdAt || "");

function copiesOf(sessions: SharedSession[]): SharedWorkingCopy[] {
  const byId = new Map<string, SharedWorkingCopy>();
  for (const s of sessions) {
    const id = s.workingCopyId || `session:${s.name}`; // working copy 不明なら1セッション=1ノード
    let copy = byId.get(id);
    if (!copy) {
      copy = { workingCopyId: id, repo: s.repo || s.name, worktree: !!s.worktree, parent: s.parent || "", sessions: [] };
      byId.set(id, copy);
    }
    copy.sessions.push(s);
  }
  for (const c of byId.values()) c.sessions.sort(byCreatedDesc);
  return [...byId.values()];
}

// groupedRepos(lib/project.ts)と同じアルゴリズム: base を名前順、worktree は
// parent==base.repo でグルーピングして作成順、親不明の worktree は孤立グループ。
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
  for (const o of orphans.sort(byCreated)) groups.push([o]);
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
    for (const group of groupedCopies(copiesOf(byOwner.get(owner) || []))) {
      const head = group[0];
      out.push({ ownerUserKey: owner, projectName: head.worktree ? head.parent || head.repo : head.repo, copies: group });
    }
  }
  return out;
}
