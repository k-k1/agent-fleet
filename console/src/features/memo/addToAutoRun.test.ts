import { describe, expect, it } from "vitest";
import { categoryChoices, MEMO_ONLY_PREFIX } from "./addToAutoRun.ts";
import type { MemoCategory, MemoQueue } from "../../types/memo.ts";

const cat = (id: string, position: number, over: Partial<MemoCategory> = {}): MemoCategory => ({ id, repo: "app", name: id, position, createdAt: "", ...over });

describe("categoryChoices", () => {
  it("puts the auto-run categories first, each group in the memo list's order, and keeps to one repository", () => {
    const cats = [cat("plain-a", 0), cat("run-b", 2, { queue: true }), cat("run-a", 1, { queue: true }), cat("plain-b", 3), cat("other", 0, { repo: "web", queue: true })];
    const got = categoryChoices(cats, [], "app");
    expect(got.map((c) => c.key)).toEqual(["run-a", "run-b", "plain-a", "plain-b"]);
    expect(got.map((c) => c.autoRun)).toEqual([true, true, false, false]);
  });

  it("counts a category with switched-off settings as auto-run, marked off", () => {
    const q = { categoryId: "x", enabled: false } as MemoQueue;
    expect(categoryChoices([cat("x", 0)], [q], "app")).toMatchObject([{ autoRun: true, off: true }]);
  });

  it("appends the names only notes carry, in note order, as ordinary choices with a key that is no table id", () => {
    const memos = [
      { repo: "app", category: "" },
      { repo: "app", category: "plain-a" },
      { repo: "app", category: "legacy-2" },
      { repo: "app", category: "legacy-1" },
      { repo: "app", category: "legacy-2" },
      { repo: "web", category: "other-repo" },
    ];
    const got = categoryChoices([cat("plain-a", 0), cat("run-a", 1, { queue: true })], [], "app", memos);
    expect(got.map((c) => c.name)).toEqual(["run-a", "plain-a", "legacy-2", "legacy-1"]);
    expect(got.slice(2)).toEqual([
      { key: MEMO_ONLY_PREFIX + "legacy-2", name: "legacy-2", autoRun: false, off: false },
      { key: MEMO_ONLY_PREFIX + "legacy-1", name: "legacy-1", autoRun: false, off: false },
    ]);
  });

  it("offers a repository that has only memo-only names", () => {
    expect(categoryChoices([], [], "app", [{ repo: "app", category: "x" }]).map((c) => c.name)).toEqual(["x"]);
  });
});
