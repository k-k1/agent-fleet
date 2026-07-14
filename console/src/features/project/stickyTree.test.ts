import { describe, expect, it } from "vitest";
import { stickyAncestors } from "./stickyTree.ts";

const rows = [
  { path: "repo", type: "dir", depth: 0 },
  { path: "repo/src", type: "dir", depth: 1 },
  { path: "repo/src/a.ts", type: "file", depth: 2 },
  { path: "repo/test", type: "dir", depth: 1 },
  { path: "repo/test/a.test.ts", type: "file", depth: 2 },
  { path: "other", type: "dir", depth: 0 },
];

describe("stickyAncestors", () => {
  it("returns the directory lineage of the row at the viewport edge", () => {
    expect(stickyAncestors(rows, 2).map((r) => r.path)).toEqual(["repo", "repo/src"]);
  });

  it("replaces a completed sibling branch", () => {
    expect(stickyAncestors(rows, 4).map((r) => r.path)).toEqual(["repo", "repo/test"]);
    expect(stickyAncestors(rows, 5).map((r) => r.path)).toEqual(["other"]);
  });

  it("honours the visible row limit", () => {
    expect(stickyAncestors(rows, 2, 1).map((r) => r.path)).toEqual(["repo"]);
    expect(stickyAncestors(rows, 2, 0)).toEqual([]);
  });
});
