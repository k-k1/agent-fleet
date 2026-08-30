// sharedProject — groups the flat 受信側 SharedSession[] into a project/worktree
// tree, mirroring lib/project.ts groupedRepos() (docs/log/59)。所有者側と違い working
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
  /** チェックアウト中のブランチ(不明なら "")。worktree の見出しはこれを名前に使う。 */
  branch: string;
  sessions: SharedSession[];
}

export interface SharedProjectGroup {
  /** グルーピング／永続キーの同一性はこちら(正規化キー)。表示は ownerEmail 側。 */
  ownerUserKey: string;
  /** 所有者のログイン ID(メールアドレス)。未設定の identity では空。 */
  ownerEmail?: string;
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
      copy = { workingCopyId: id, repo: s.repo || s.name, worktree: !!s.worktree, parent: s.parent || "",
        branch: s.branch || "", sessions: [] };
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
  // 親が共有されていない worktree 群は、親の名前ごとに1グループへまとめる。所有者側と
  // 違い、受信側ではベース作業コピーが共有に含まれないことが普通にある(セッションを
  // worktree で切る運用ではベース直下に共有対象のセッションが無い)。1本ずつ独立した
  // グループにすると、同じプロジェクト名の見出しが worktree の数だけ並んでしまう。
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
    // email は identity 単位なので、この所有者のどの行から取っても同じ。
    const ownerEmail = owned.find((s) => s.ownerEmail)?.ownerEmail;
    for (const group of groupedCopies(copiesOf(owned))) {
      const head = group[0];
      out.push({ ownerUserKey: owner, ownerEmail, projectName: head.worktree ? head.parent || head.repo : head.repo, copies: group });
    }
  }
  return out;
}
