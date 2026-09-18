// The mode a row gets is the whole substance of this dialog, so each rule is pinned with the
// session state it stands for. Two of them are the ones a "simplification" would merge:
// "busy" is not "alive" (a session parked on a question is not computing anything, and the
// arm would never fire on it), and a shell is not an idle agent (af cannot see its command,
// so it is never ticked for the user).
import { describe, it, expect } from "vitest";
import {
  planStopTree,
  defaultStopSelection,
  stopModeOf,
  stopOrder,
  stopRows,
  summarizeStop,
  liveSessionCount,
} from "./stopTree.ts";
import type { Repo } from "./store.ts";
import type { RepoTreeNode } from "../../lib/project.ts";
import type { Session } from "../../types/session.ts";

const repo = (name: string, over: Partial<Repo> = {}): Repo => ({
  name,
  worktree: true,
  parent: "app",
  branch: "temp/" + name.split("@")[1],
  ...over,
});

const node = (r: Repo, children: RepoTreeNode[] = []): RepoTreeNode => ({ repo: r, children, spine: "" });

const sess = (name: string, folder: string, over: Partial<Session> = {}): Session => ({
  name,
  kind: "claude",
  repo: folder,
  alive: true,
  ...over,
});

const rowNamed = (groups: ReturnType<typeof planStopTree>, name: string) =>
  stopRows(groups).find((r) => r.session.name === name)!;

/** An hour out — far enough that the pin is still live whenever this runs. */
const soon = () => new Date(Date.now() + 3600_000).toISOString();

describe("停止計画の並べ方", () => {
  it("右クリックした作業コピーが先頭、子はその下（rail と同じ順）", () => {
    const tree = node(repo("app@a"), [node(repo("app@b")), node(repo("app@c"))]);
    const groups = planStopTree(tree, [sess("s1", "app@a"), sess("s2", "app@b"), sess("s3", "app@c")]);
    expect(groups.map((g) => g.repo.name)).toEqual(["app@a", "app@b", "app@c"]);
    expect(groups.map((g) => g.depth)).toEqual([0, 1, 1]);
  });

  it("停止中のセッションは行にならず、動いているセッションが無い作業コピーは並ばない", () => {
    const tree = node(repo("app@a"), [node(repo("app@b"))]);
    const groups = planStopTree(tree, [sess("s1", "app@a", { alive: false }), sess("s2", "app@b")]);
    expect(groups.map((g) => g.repo.name)).toEqual(["app@b"]);
    expect(liveSessionCount(tree, [sess("s1", "app@a", { alive: false })])).toBe(0);
  });

  it("実行対象はチェックした行だけ（並び順は表示のまま）", () => {
    const groups = planStopTree(node(repo("app@a")), [sess("s1", "app@a"), sess("s2", "app@a")]);
    expect(stopOrder(groups, new Set(["s2"])).map((r) => r.session.name)).toEqual(["s2"]);
  });
});

describe("行ごとの停止のしかた", () => {
  it("待機中はすぐ停止、実行中はターン終了後（既定ではターンを切らない）", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("idle", "app@a"),
      sess("busy", "app@a", { state: "working" }),
    ]);
    expect(stopModeOf(rowNamed(groups, "idle"), false)).toBe("now");
    expect(stopModeOf(rowNamed(groups, "busy"), false)).toBe("after");
  });

  it("バックグラウンド作業中も実行中として扱う（idle と報告されていても切らない）", () => {
    const groups = planStopTree(node(repo("app@a")), [sess("bg", "app@a", { state: "idle", backgroundBusy: true })]);
    expect(stopModeOf(rowNamed(groups, "bg"), false)).toBe("after");
  });

  it("質問・上限待ちはターンが終わらないので予約せずすぐ停止する", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("q", "app@a", { state: "question" }),
      sess("lim", "app@a", { state: "limited" }),
    ]);
    expect(stopModeOf(rowNamed(groups, "q"), false)).toBe("now");
    expect(stopModeOf(rowNamed(groups, "lim"), false)).toBe("now");
  });

  it("shell はターンの概念が無いので予約に回さない", () => {
    const groups = planStopTree(node(repo("app@a")), [sess("sh", "app@a", { kind: "shell" })]);
    expect(rowNamed(groups, "sh").canArm).toBe(false);
    expect(stopModeOf(rowNamed(groups, "sh"), false)).toBe("now");
  });

  it("実行中に見える shell でも予約しない（予約は永久に発火せず、止め損ねになる）", () => {
    // The wire should never say this today (shell emits no state), but "busy" and "can end a
    // turn" are different questions and only the second decides the arm. Reading the first
    // alone would leave such a row running with a promise nothing keeps.
    const groups = planStopTree(node(repo("app@a")), [sess("sh", "app@a", { kind: "shell", state: "working" })]);
    expect(stopModeOf(rowNamed(groups, "sh"), false)).toBe("now");
  });

  it("「すぐ停止」を選ぶと実行中もその場で止める", () => {
    const groups = planStopTree(node(repo("app@a")), [sess("busy", "app@a", { state: "working" })]);
    expect(stopModeOf(rowNamed(groups, "busy"), true)).toBe("now");
  });
});

describe("既定のチェック", () => {
  it("待機中も実行中も既定で入る（実行中は予約なので何も失われない）", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("idle", "app@a"),
      sess("busy", "app@a", { state: "working" }),
    ]);
    expect([...defaultStopSelection(groups)].sort()).toEqual(["busy", "idle"]);
  });

  it("shell と、起きたままに固定したセッションは既定で入らない", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("sh", "app@a", { kind: "shell" }),
      sess("pin", "app@a", { keepAwakeUntil: soon() }),
      sess("idle", "app@a"),
    ]);
    expect([...defaultStopSelection(groups)]).toEqual(["idle"]);
    expect(rowNamed(groups, "sh").whyKey).toBe("rp.stop.why_opaque");
    expect(rowNamed(groups, "pin").whyKey).toBe("rp.stop.why_pinned");
  });

  it("期限切れの固定は固定として扱わない（バッジと同じ判定）", () => {
    const past = new Date(Date.now() - 60_000).toISOString();
    const groups = planStopTree(node(repo("app@a")), [sess("old", "app@a", { keepAwakeUntil: past })]);
    expect(rowNamed(groups, "old").pinned).toBe("");
    expect([...defaultStopSelection(groups)]).toEqual(["old"]);
  });

  it("既に予約済みの行は、そう言う", () => {
    const groups = planStopTree(node(repo("app@a")), [sess("armed", "app@a", { stopAfterTurnAt: soon() })]);
    expect(rowNamed(groups, "armed").whyKey).toBe("rp.stop.why_armed");
  });
});

describe("フッターの数え方", () => {
  it("実行中はターン終了後に回り、切られる件数は 0 のまま", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("idle", "app@a"),
      sess("busy", "app@a", { state: "working" }),
    ]);
    const sum = summarizeStop(groups, new Set(["idle", "busy"]), false);
    expect(sum).toMatchObject({ total: 2, now: 1, after: 1, cut: 0, busy: 1 });
  });

  it("「すぐ停止」を入れると、切られる件数がそのまま警告の数になる", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("idle", "app@a"),
      sess("busy", "app@a", { state: "working" }),
    ]);
    const sum = summarizeStop(groups, new Set(["idle", "busy"]), true);
    expect(sum).toMatchObject({ total: 2, now: 2, after: 0, cut: 1, busy: 1 });
  });

  it("チェックを外した実行中は数に入らない", () => {
    const groups = planStopTree(node(repo("app@a")), [
      sess("idle", "app@a"),
      sess("busy", "app@a", { state: "working" }),
    ]);
    expect(summarizeStop(groups, new Set(["idle"]), false)).toMatchObject({ total: 1, busy: 0, after: 0 });
  });
});
