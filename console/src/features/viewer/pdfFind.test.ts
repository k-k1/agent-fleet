import { describe, expect, it } from "vitest";
import { findMatches, firstMatchFrom, foldQuery, pageText, stepMatch } from "./pdfFind.ts";

describe("pageText / findMatches", () => {
  it("maps a match back to the item and offset of its first and last character", () => {
    const pages = [pageText(["Error ", "code 3002", " exceeded"])];
    expect(findMatches(pages, "3002")).toEqual([{ page: 0, startItem: 1, startOffset: 5, endItem: 1, endOffset: 9 }]);
  });

  it("matches a word wrapped across two items, whatever the spacing", () => {
    const pages = [pageText(["SMS送信", "可能です"]), pageText(["send request", "limit"])];
    expect(findMatches(pages, "送信可能")).toEqual([{ page: 0, startItem: 0, startOffset: 3, endItem: 1, endOffset: 2 }]);
    expect(findMatches(pages, "request limit")).toHaveLength(1);
    expect(findMatches(pages, "requestlimit")).toHaveLength(1);
  });

  it("ignores case and folds Kangxi radicals to ideographs", () => {
    const pages = [pageText(["⽇本語の⾒出し", "Expire URL"])];
    expect(findMatches(pages, "日本語")).toHaveLength(1);
    expect(findMatches(pages, "expire url")).toHaveLength(1);
  });

  it("lists every match in reading order without overlaps", () => {
    const pages = [pageText(["aaa"]), pageText([]), pageText(["a", "a"])];
    expect(findMatches(pages, "aa").map((m) => [m.page, m.startOffset])).toEqual([[0, 0], [2, 0]]);
  });

  it("finds nothing for an empty or blank query", () => {
    const pages = [pageText(["text"])];
    expect(findMatches(pages, "")).toEqual([]);
    expect(findMatches(pages, "  ")).toEqual([]);
    expect(foldQuery(" A b ")).toBe("ab");
  });
});

describe("firstMatchFrom / stepMatch", () => {
  const m = (page: number) => ({ page, startItem: 0, startOffset: 0, endItem: 0, endOffset: 1 });

  it("starts at the first match on or after the page being read, else wraps to the first", () => {
    const list = [m(0), m(3), m(5)];
    expect(firstMatchFrom(list, 3)).toBe(1);
    expect(firstMatchFrom(list, 4)).toBe(2);
    expect(firstMatchFrom(list, 9)).toBe(0);
    expect(firstMatchFrom([], 0)).toBe(-1);
  });

  it("wraps at both ends", () => {
    expect(stepMatch(2, 3, 1)).toBe(0);
    expect(stepMatch(0, 3, -1)).toBe(2);
    expect(stepMatch(-1, 3, -1)).toBe(2);
    expect(stepMatch(0, 0, 1)).toBe(-1);
  });
});
