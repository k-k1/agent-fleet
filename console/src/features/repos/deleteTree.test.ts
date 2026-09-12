// The grading is what the modal's default tick means, so each rule is pinned with the
// state it stands for. The two that carry real risk are the ones a "simplification" would
// merge: a blocked row is not a review row with a scarier colour (no flag gets past it),
// and the base clone's own block depends on the SELECTION, not on its state.
import { describe, it, expect } from "vitest";
import { planTree, defaultSelection, deleteOrder, baseBlockedByWorktrees, summarize } from "./deleteTree.ts";
import type { Repo } from "./store.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";

const repo = (name: string, over: Partial<Repo> = {}): Repo => ({
  name,
  worktree: true,
  parent: "app",
  branch: "temp/" + name.split("@")[1],
  integration: { relation: "contained", targetUnique: 47, worktreeUnique: 0 },
  ...over,
});

const node = (r: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo: r, children, spine: "" });

const sess = (name: string, folder: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  repo: folder,
  ...over,
});

/** The screenshot's shape: a base, a live parent worktree, four finished children. */
const tree = () =>
  node(repo("app@a", { integration: { relation: "contained", targetUnique: 1, worktreeUnique: 0 } }), [
    node(repo("app@b")),
    node(repo("app@c")),
  ]);

describe("削除計画の並べ方", () => {
  it("右クリックした作業コピーが先頭、子はその下（rail と同じ順）", () => {
    const plans = planTree(tree(), []);
    expect(plans.map((p) => p.repo.name)).toEqual(["app@a", "app@b", "app@c"]);
    expect(plans.map((p) => p.depth)).toEqual([0, 1, 1]);
  });

  it("実行順は深い方から（親が先に消えて子が孤児になるのを防ぐ）", () => {
    const deep = node(repo("app@a"), [node(repo("app@b"), [node(repo("app@c"))])]);
    const plans = planTree(deep, []);
    const order = deleteOrder(plans, new Set(["app@a", "app@b", "app@c"]));
    expect(order.map((p) => p.repo.name)).toEqual(["app@c", "app@b", "app@a"]);
  });

  it("止められない行は実行対象から外れる（force でも通らないため）", () => {
    const plans = planTree(node(repo("app@a"), [node(repo("app@b", { locked: true }))]), []);
    const order = deleteOrder(plans, new Set(["app@a", "app@b"]));
    expect(order.map((p) => p.repo.name)).toEqual(["app@a"]);
  });
});

describe("作業コピーの判定", () => {
  it("親に含まれ・未コミット無し・セッションが動いていなければ安全", () => {
    const [p] = planTree(node(repo("app@a")), [sess("s1", "app@a")]);
    expect(p.grade).toBe("safe");
    expect(p.force).toBe(false);
    expect(p.archive.map((s) => s.name)).toEqual(["s1"]);
  });

  it("稼働中のセッションがあれば不可（ロックより後、未コミットより先に見る）", () => {
    const [p] = planTree(node(repo("app@a", { dirty: true })), [sess("s1", "app@a", { alive: true })]);
    expect(p.grade).toBe("blocked");
    expect(p.whyKey).toBe("rp.del.why_alive");
    expect(p.whyCount).toBe(1);
  });

  it("コピー自身のロックが最優先の理由（Agent が真っ先に 403 を返す順）", () => {
    const [p] = planTree(node(repo("app@a", { locked: true })), [sess("s1", "app@a", { alive: true })]);
    expect(p.whyKey).toBe("rp.del.why_locked");
  });

  it("ロックされたセッションが中にいれば不可", () => {
    const [p] = planTree(node(repo("app@a")), [sess("s1", "app@a", { locked: true })]);
    expect(p.grade).toBe("blocked");
    expect(p.whyKey).toBe("rp.del.why_session_locked");
    // A locked session is not archived on the way out either — the row never runs.
    expect(p.archive).toEqual([]);
  });

  it("未コミットは要確認で、削除には force が要る", () => {
    const [p] = planTree(node(repo("app@a", { dirty: true })), []);
    expect(p.grade).toBe("review");
    expect(p.whyKey).toBe("rp.del.why_dirty");
    expect(p.force).toBe(true);
  });

  it("親に無いコミットは要確認（件数つき）", () => {
    const [p] = planTree(
      node(repo("app@a", { integration: { relation: "unmerged", targetUnique: 0, worktreeUnique: 3 } })),
      [],
    );
    expect(p.grade).toBe("review");
    expect(p.whyKey).toBe("rp.del.why_unmerged");
    expect(p.whyCount).toBe(3);
  });

  it("未 push も要確認で force 対象（Agent の dirty||ahead ガードと同じ条件）", () => {
    const [p] = planTree(node(repo("app@a", { ahead: 2 })), []);
    expect(p.whyKey).toBe("rp.del.why_unpushed");
    expect(p.force).toBe(true);
  });

  it("shell は棚に上げず忘れる（会話が無いため）", () => {
    const [p] = planTree(node(repo("app@a")), [sess("s1", "app@a", { kind: "shell" }), sess("s2", "app@a")]);
    expect(p.forget.map((s) => s.name)).toEqual(["s1"]);
    expect(p.archive.map((s) => s.name)).toEqual(["s2"]);
  });

  it("既定でチェックが入るのは安全な行だけ", () => {
    const plans = planTree(node(repo("app@a", { dirty: true }), [node(repo("app@b"))]), []);
    expect([...defaultSelection(plans)]).toEqual(["app@b"]);
  });
});

describe("ブランチを道連れにできる条件", () => {
  it("親に含まれる worktree のブランチだけ、親の作業コピーで消す", () => {
    const [p] = planTree(node(repo("app@a")), []);
    expect(p.branch).toBe("temp/a");
    expect(p.branchIn).toBe("app");
  });

  it("親に無いコミットを持つブランチは対象外", () => {
    const [p] = planTree(
      node(repo("app@a", { integration: { relation: "unmerged", targetUnique: 0, worktreeUnique: 1 } })),
      [],
    );
    expect(p.branch).toBe("");
  });

  it("本体クローンのブランチは対象外（道連れにするのは worktree の使い捨てブランチだけ）", () => {
    const [p] = planTree(node(repo("app", { worktree: false, parent: undefined, branch: "develop" })), []);
    expect(p.branch).toBe("");
  });
});

describe("本体クローンは worktree が残っている限り消せない", () => {
  const base = () =>
    planTree(node(repo("app", { worktree: false, parent: undefined, branch: "develop" }), [node(repo("app@b"))]), []);

  it("worktree を1つでも残すなら本体は今回消せない", () => {
    expect(baseBlockedByWorktrees(base(), new Set(["app"]))).toBe(true);
  });

  it("全部まとめて消すなら本体も消せる", () => {
    expect(baseBlockedByWorktrees(base(), new Set(["app", "app@b"]))).toBe(false);
  });

  it("本体を選んでいなければ関係ない", () => {
    expect(baseBlockedByWorktrees(base(), new Set(["app@b"]))).toBe(false);
  });

  it("worktree を右クリックした場合は関係ない（git の worktree は横並びで、入れ子ではない）", () => {
    const plans = planTree(node(repo("app@a"), [node(repo("app@b"))]), []);
    expect(baseBlockedByWorktrees(plans, new Set(["app@a"]))).toBe(false);
  });
});

describe("実行前の内訳", () => {
  it("選択された行のぶんだけ数える", () => {
    const plans = planTree(node(repo("app@a", { dirty: true }), [node(repo("app@b")), node(repo("app@c"))]), [
      sess("s1", "app@b"),
      sess("s2", "app@c", { kind: "shell" }),
    ]);
    const sum = summarize(plans, new Set(["app@a", "app@b"]), true);
    expect(sum).toEqual({ copies: 2, archive: 1, forget: 0, branches: 2, force: 1 });
  });

  it("ブランチを消さない設定なら 0 件", () => {
    const plans = planTree(node(repo("app@a")), []);
    expect(summarize(plans, new Set(["app@a"]), false).branches).toBe(0);
  });
});
