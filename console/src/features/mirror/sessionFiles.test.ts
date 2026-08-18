// docs/68 の突合そのもの——転写が「編集した」と言っているファイルに、git が今どう
// 見えているかを重ねる部分。ここが崩れると一覧は嘘をつくが、画面上は普通に見える。
import { describe, it, expect, vi, beforeEach } from "vitest";

const openFileDiff = vi.fn();
const openFileMode = vi.fn();
const openTargetInNew = vi.fn();
vi.mock("../scm/open.ts", () => ({ openFileDiff: (...a: unknown[]) => openFileDiff(...a), openRepoScm: vi.fn() }));
vi.mock("../viewer/openFile.ts", () => ({ openFileMode: (...a: unknown[]) => openFileMode(...a) }));
vi.mock("../../layout/store.ts", () => ({
  useLayoutStore: { getState: () => ({ openTargetInNew: (...a: unknown[]) => openTargetInNew(...a) }) },
}));

import { joinChanges, openRow, sortRows, stateBadge, type FsChange, type SessionFile } from "./sessionFiles.ts";

const file = (over: Partial<SessionFile> = {}): SessionFile => ({
  path: "repos/r/src/a.ts",
  repo: "r",
  rel: "src/a.ts",
  verb: "edit",
  count: 1,
  lastIdx: 1,
  lastTs: "2026-08-17T10:00:00Z",
  ...over,
});

const change = (over: Partial<FsChange> = {}): FsChange => ({
  path: "repos/r/src/a.ts",
  repo: "r",
  index: " ",
  worktree: "M",
  ...over,
});

describe("joinChanges", () => {
  it("矛盾する path 表現ではなく (repo, rel) で突き合わせる", () => {
    // /fs/changes は必ず repos/<repo>/<rel>、転写側の path は browse root 基準
    // （AF_BROWSE_ROOT で動く）。既定では一致するので、path で突き合わせる実装は
    // ここでは通ってしまう——だから rel を食い違わせて鍵の方を確かめる。
    const rows = joinChanges([file({ path: "elsewhere/src/a.ts" })], [change()]);
    expect(rows[0].state).toBe("unstaged");
  });

  it("git の見え方をそのまま状態にする", () => {
    const rows = joinChanges(
      [
        file({ rel: "u.ts", path: "repos/r/u.ts" }),
        file({ rel: "s.ts", path: "repos/r/s.ts" }),
        file({ rel: "w.ts", path: "repos/r/w.ts" }),
      ],
      [
        change({ path: "repos/r/u.ts", untracked: true, index: "?", worktree: "?" }),
        change({ path: "repos/r/s.ts", index: "M", worktree: " " }),
        change({ path: "repos/r/w.ts", index: " ", worktree: "M" }),
      ],
    );
    expect(rows.map((r) => r.state)).toEqual(["untracked", "staged", "unstaged"]);
  });

  it("⚠️ 作業ツリーに差分が無い行を落とさない（落とすと『さっき直したのに居ない』になる）", () => {
    const rows = joinChanges([file()], []);
    expect(rows).toHaveLength(1);
    expect(rows[0].state).toBe("clean");
  });

  it("差分は無いがコミットに現れた行は「コミット済み」になる", () => {
    expect(joinChanges([file()], [], ["src/a.ts"])[0].state).toBe("committed");
  });

  it("⚠️ コミットに無いものを「取り消された」と断じない——差分なしのまま", () => {
    // 差分が無くコミットにも無い理由は他にもある（開始より前のコミットに入っていた・
    // 別の作業コピーで起きた）。肯定できるのは「コミットに現れた」ことだけ。
    expect(joinChanges([file()], [], ["other/z.ts"])[0].state).toBe("clean");
  });

  it("作業ツリーに差分がある行はコミット集合より git を優先する", () => {
    // 「コミット済み、ただしその後また直した」は、いま開けるのは作業差分の方。
    expect(joinChanges([file()], [change()], ["src/a.ts"])[0].state).toBe("unstaged");
  });

  it("作業コピーの外は列挙するが git 側は持たない", () => {
    const rows = joinChanges([file({ path: ".claude/settings.json", repo: undefined, rel: undefined })], []);
    expect(rows[0].state).toBe("outside");
    expect(rows[0].name).toBe("settings.json");
  });

  it("名前とディレクトリを分ける（行の主役はファイル名）", () => {
    const rows = joinChanges([file({ rel: "src/features/a.ts" })], []);
    expect(rows[0].name).toBe("a.ts");
    expect(rows[0].dir).toBe("src/features");
  });

  it("削除は転写側の verb でも git の D でも印が付く", () => {
    const byVerb = joinChanges([file({ verb: "delete" })], []);
    const byGit = joinChanges([file()], [change({ index: "D", worktree: " " })]);
    expect(byVerb[0].deleted).toBe(true);
    expect(byGit[0].deleted).toBe(true);
  });
});

describe("sortRows", () => {
  const rows = joinChanges(
    [
      file({ rel: "b.ts", path: "repos/r/b.ts", lastTs: "2026-08-17T10:00:00Z", lastIdx: 1 }),
      file({ rel: "a.ts", path: "repos/r/a.ts", lastTs: "2026-08-17T12:00:00Z", lastIdx: 2 }),
    ],
    [],
  );

  it("既定は新しい順（『さっき直したやつ』が最頻の用）", () => {
    expect(sortRows(rows, "recent").map((r) => r.rel)).toEqual(["a.ts", "b.ts"]);
  });

  it("パス順にも切り替えられる", () => {
    expect(sortRows(rows, "path").map((r) => r.rel)).toEqual(["a.ts", "b.ts"]);
  });
});

describe("openRow", () => {
  beforeEach(() => {
    openFileDiff.mockClear();
    openFileMode.mockClear();
    openTargetInNew.mockClear();
  });

  it("差分があるものは差分で開く", () => {
    openRow(joinChanges([file()], [change()])[0]);
    expect(openFileDiff).toHaveBeenCalledWith("r", "src/a.ts", false);
  });

  it("⚠️ 未追跡は差分が空なのでファイルを開く", () => {
    openRow(joinChanges([file()], [change({ untracked: true })])[0]);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("コミット済みの行はファイルとして開ける（作業差分は無い）", () => {
    openRow(joinChanges([file()], [], ["src/a.ts"])[0]);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("差分なしの行もファイルとしては開ける", () => {
    openRow(joinChanges([file()], [])[0]);
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("消えたファイルは開かない（開く先が無い）", () => {
    openRow(joinChanges([file({ verb: "delete" })], [])[0]);
    expect(openFileMode).not.toHaveBeenCalled();
    expect(openTargetInNew).not.toHaveBeenCalled();
  });

  it("split は別ペインへ", () => {
    openRow(joinChanges([file()], [change()])[0], true);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openTargetInNew).toHaveBeenCalledWith(
      { content: { kind: "wtdiff", scmRepo: "r", filePath: "src/a.ts", diffStaged: false } },
      true,
    );
  });
});

describe("stateBadge", () => {
  it("差分が無いものと作業コピー外は目立たせない", () => {
    expect(stateBadge("clean").cls).toBe("st-muted");
    expect(stateBadge("outside").cls).toBe("st-muted");
    expect(stateBadge("unstaged").cls).toBe("st-mod");
  });

  it("コミット済みは差分なしと別の見た目にする（同じ灰色だと P2 の意味が消える）", () => {
    expect(stateBadge("committed").cls).not.toBe(stateBadge("clean").cls);
    expect(stateBadge("committed").label).not.toBe(stateBadge("clean").label);
  });
});
