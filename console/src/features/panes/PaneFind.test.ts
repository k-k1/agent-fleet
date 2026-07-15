import { describe, expect, it } from "vitest";
import { matchOffsets } from "./PaneFind.tsx";

describe("matchOffsets", () => {
  it("finds every non-overlapping match case-insensitively", () => {
    expect(matchOffsets("Alpha beta ALPHA", "alpha")).toEqual([
      { start: 0, end: 5 },
      { start: 11, end: 16 },
    ]);
  });

  it("treats regular-expression characters as literal text", () => {
    expect(matchOffsets("a.b a-b a.b", "a.b")).toEqual([
      { start: 0, end: 3 },
      { start: 8, end: 11 },
    ]);
  });

  it("supports Japanese queries and an empty query", () => {
    expect(matchOffsets("検索して、もう一度検索", "検索")).toEqual([
      { start: 0, end: 2 },
      { start: 9, end: 11 },
    ]);
    expect(matchOffsets("text", "")).toEqual([]);
  });
});
