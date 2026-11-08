import { describe, expect, it } from "vitest";
import { lineToTop, monotoneAnchors, topToLine } from "./splitScrollSync.ts";
import { insertedLineMap } from "./mdSourceLines.ts";
import { repairFullwidthTables } from "../markdown.ts";

const anchors = [
  { line: 1, top: 0 },
  { line: 5, top: 100 },
  { line: 9, top: 500 },
  { line: 21, top: 900 },
];

describe("split scroll mapping", () => {
  it("interpolates between anchors", () => {
    expect(lineToTop(anchors, 3)).toBe(50);
    expect(lineToTop(anchors, 7)).toBe(300);
    expect(lineToTop(anchors, 15)).toBe(700);
  });

  it("clamps outside the anchored range", () => {
    expect(lineToTop(anchors, 0)).toBe(0);
    expect(lineToTop(anchors, 99)).toBe(900);
    expect(topToLine(anchors, -10)).toBe(1);
    expect(topToLine(anchors, 5000)).toBe(21);
  });

  it("topToLine inverts lineToTop", () => {
    for (const line of [1, 2.5, 5, 8.75, 13, 20.9]) {
      expect(topToLine(anchors, lineToTop(anchors, line))).toBeCloseTo(line, 9);
    }
  });

  it("drops anchors that would run the mapping backwards", () => {
    const out = monotoneAnchors([
      { line: 1, top: 0 },
      { line: 4, top: 300 },
      { line: 3, top: 400 }, // a later offset for an earlier line
      { line: 6, top: 250 }, // an earlier offset for a later line
      { line: 8, top: 600 },
      { line: 8, top: 650 }, // same line twice
    ]);
    expect(out).toEqual([
      { line: 1, top: 0 },
      { line: 3, top: 400 },
      { line: 8, top: 600 },
    ]);
  });
});

describe("insertedLineMap", () => {
  it("maps a repaired body's lines back past the delimiter rows the repair inserted", () => {
    const body = ["intro", "", "｜a｜b｜", "｜1｜2｜", "｜3｜4｜", "｜5｜6｜", "", "after"].join("\n");
    const repair = repairFullwidthTables(body)!;
    expect(repair.inserted).toEqual([3]);
    const lines = repair.body.split("\n");
    expect(lines[3]).toBe("|---|---|");
    const map = insertedLineMap(repair.inserted);
    expect(lines.indexOf("after")).toBe(8);
    expect(map(8)).toBe(7); // "after" is line 7 of what the user wrote
    expect(map(2)).toBe(2); // above the insertion nothing moves
    expect(map(3)).toBe(2); // the inserted row sits with its header
  });

  it("reports no insertion when the table already had a delimiter row", () => {
    const repair = repairFullwidthTables("｜a｜b｜\n｜－｜－｜\n｜1｜2｜")!;
    expect(repair.inserted).toEqual([]);
  });
});
