// The join at the heart of docs/log/68: overlaying git's current view onto the files the
// transcript says were edited. When this breaks the list lies, and the screen looks perfectly
// normal.
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
  it("joins on (repo, rel) rather than on path representations that disagree", () => {
    // /fs/changes always reports repos/<repo>/<rel>, while the transcript's path is relative to
    // the browse root (which AF_BROWSE_ROOT moves). They agree by default, so an implementation
    // that joins on path would pass here - hence the deliberately mismatched rel, to check the key.
    const rows = joinChanges([file({ path: "elsewhere/src/a.ts" })], [change()]);
    expect(rows[0].state).toBe("unstaged");
  });

  it("takes the state straight from what git reports", () => {
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

  it("keeps rows with no working-tree diff (dropping them reads as 'I just edited that and it is gone')", () => {
    const rows = joinChanges([file()], []);
    expect(rows).toHaveLength(1);
    expect(rows[0].state).toBe("clean");
  });

  it("marks a row with no diff that appeared in a commit as committed", () => {
    expect(joinChanges([file()], [], ["src/a.ts"])[0].state).toBe("committed");
  });

  it("never declares a row absent from the commits as reverted; it stays No diff", () => {
    // There are other reasons for no diff and no commit (it landed in a commit made before the
    // session started, or it happened in another working copy). The only positive claim that can
    // be made is that the path appeared in a commit.
    expect(joinChanges([file()], [], ["other/z.ts"])[0].state).toBe("clean");
  });

  it("prefers git over the commit set for a row with a working-tree diff", () => {
    // For "committed, then edited again", the thing to open now is the working-tree diff.
    expect(joinChanges([file()], [change()], ["src/a.ts"])[0].state).toBe("unstaged");
  });

  it("lists files outside a working copy but gives them no git side", () => {
    const rows = joinChanges([file({ path: ".claude/settings.json", repo: undefined, rel: undefined })], []);
    expect(rows[0].state).toBe("outside");
    expect(rows[0].name).toBe("settings.json");
  });

  it("splits name from directory (the file name is the row's main label)", () => {
    const rows = joinChanges([file({ rel: "src/features/a.ts" })], []);
    expect(rows[0].name).toBe("a.ts");
    expect(rows[0].dir).toBe("src/features");
  });

  it("marks a deletion from the transcript verb and from git's D alike", () => {
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

  it("defaults to newest first, since 'the one I just edited' is the common case", () => {
    expect(sortRows(rows, "recent").map((r) => r.rel)).toEqual(["a.ts", "b.ts"]);
  });

  it("can switch to path order", () => {
    expect(sortRows(rows, "path").map((r) => r.rel)).toEqual(["a.ts", "b.ts"]);
  });
});

describe("openRow", () => {
  beforeEach(() => {
    openFileDiff.mockClear();
    openFileMode.mockClear();
    openTargetInNew.mockClear();
  });

  it("opens the diff for a row that has one", () => {
    openRow(joinChanges([file()], [change()])[0]);
    expect(openFileDiff).toHaveBeenCalledWith("r", "src/a.ts", false);
  });

  it("opens the file for an untracked row, whose diff is empty", () => {
    openRow(joinChanges([file()], [change({ untracked: true })])[0]);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("opens a committed row as a file (there is no working-tree diff)", () => {
    openRow(joinChanges([file()], [], ["src/a.ts"])[0]);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("opens a No-diff row as a file too", () => {
    openRow(joinChanges([file()], [])[0]);
    expect(openFileMode).toHaveBeenCalledWith("repos/r/src/a.ts", "view");
  });

  it("does not open a deleted file (there is nothing to open)", () => {
    openRow(joinChanges([file({ verb: "delete" })], [])[0]);
    expect(openFileMode).not.toHaveBeenCalled();
    expect(openTargetInNew).not.toHaveBeenCalled();
  });

  it("split opens in another pane", () => {
    openRow(joinChanges([file()], [change()])[0], true);
    expect(openFileDiff).not.toHaveBeenCalled();
    expect(openTargetInNew).toHaveBeenCalledWith(
      { content: { kind: "wtdiff", scmRepo: "r", filePath: "src/a.ts", diffStaged: false } },
      true,
    );
  });
});

describe("stateBadge", () => {
  it("mutes rows with no diff and rows outside a working copy", () => {
    expect(stateBadge("clean").cls).toBe("st-muted");
    expect(stateBadge("outside").cls).toBe("st-muted");
    expect(stateBadge("unstaged").cls).toBe("st-mod");
  });

  it("styles committed differently from No diff (the same grey would erase what P2 means)", () => {
    expect(stateBadge("committed").cls).not.toBe(stateBadge("clean").cls);
    expect(stateBadge("committed").label).not.toBe(stateBadge("clean").label);
  });
});
