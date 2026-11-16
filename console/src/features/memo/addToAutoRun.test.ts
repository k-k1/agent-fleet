import { describe, expect, it } from "vitest";
import { categoryChoices } from "./addToAutoRun.ts";
import type { MemoCategory, MemoQueue } from "../../types/memo.ts";

const cat = (id: string, position: number, over: Partial<MemoCategory> = {}): MemoCategory => ({ id, repo: "app", name: id, position, createdAt: "", ...over });

describe("categoryChoices", () => {
  it("puts the auto-run categories first, each group in the memo list's order, and keeps to one repository", () => {
    const cats = [cat("plain-a", 0), cat("run-b", 2, { queue: true }), cat("run-a", 1, { queue: true }), cat("plain-b", 3), cat("other", 0, { repo: "web", queue: true })];
    const got = categoryChoices(cats, [], "app");
    expect(got.map((c) => c.category.id)).toEqual(["run-a", "run-b", "plain-a", "plain-b"]);
    expect(got.map((c) => c.autoRun)).toEqual([true, true, false, false]);
  });

  it("counts a category with switched-off settings as auto-run, marked off", () => {
    const q = { categoryId: "x", enabled: false } as MemoQueue;
    expect(categoryChoices([cat("x", 0)], [q], "app")).toMatchObject([{ autoRun: true, off: true }]);
  });
});
