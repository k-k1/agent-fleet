import { describe, expect, it } from "vitest";
import { searchHistory, stepPos } from "./historySearch.ts";

// Oldest first, as MirrorView builds it.
const HIST = ["npm test を流して", "ビルドを直して", "テストを追加", "npm run build"];

describe("searchHistory", () => {
  it("returns matches newest first", () => {
    expect(searchHistory(HIST, "npm")).toEqual([3, 0]);
  });
  it("matches a substring anywhere, case-insensitively", () => {
    expect(searchHistory(["Run NPM ci"], "npm")).toEqual([0]);
    expect(searchHistory(HIST, "テスト")).toEqual([2]);
  });
  it("treats an empty query as everything (Ctrl+R with nothing typed walks plain history)", () => {
    expect(searchHistory(HIST, "")).toEqual([3, 2, 1, 0]);
  });
  it("returns nothing when there is no hit, and for empty history", () => {
    expect(searchHistory(HIST, "デプロイ")).toEqual([]);
    expect(searchHistory([], "")).toEqual([]);
  });
});

describe("stepPos", () => {
  it("shows the newest match on the first step older (pos -1 = nothing previewed yet)", () => {
    expect(stepPos(-1, 1, 4)).toBe(0);
  });
  it("stays at -1 when stepping newer with nothing previewed", () => {
    expect(stepPos(-1, -1, 4)).toBe(-1);
  });
  it("clamps at both ends instead of wrapping", () => {
    expect(stepPos(3, 1, 4)).toBe(3);
    expect(stepPos(0, -1, 4)).toBe(0);
  });
  it("walks one step at a time", () => {
    expect(stepPos(1, 1, 4)).toBe(2);
    expect(stepPos(2, -1, 4)).toBe(1);
  });
  it("has nowhere to go with no matches", () => {
    expect(stepPos(-1, 1, 0)).toBe(-1);
    expect(stepPos(2, 1, 0)).toBe(-1);
  });
});
