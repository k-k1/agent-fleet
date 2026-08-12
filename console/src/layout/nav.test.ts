import { describe, it, expect } from "vitest";
import type { Layout } from "./types.ts";
import { paneByOrdinal, neighborPane, cyclePane } from "./nav.ts";

// Two columns × two rows. paneRows numbers them in reading order:
//   col0: a(1,r0) b(2,r1) · col1: c(3,r0) d(4,r1)
function grid(activeId: string): Layout {
  return {
    version: 3,
    mode: "split",
    cols: [
      { id: "c0", rowRatio: 0.5, cells: ["a", "b"].map((id) => ({ id, selectedViewId: null, views: [] })) },
      { id: "c1", rowRatio: 0.5, cells: ["c", "d"].map((id) => ({ id, selectedViewId: null, views: [] })) },
    ],
    colRatios: [0.5, 0.5],
    activeCellId: activeId,
  };
}

// col0 has two rows, col1 has one — for the "clamp into a shorter column" case.
function ragged(activeId: string): Layout {
  return {
    version: 3,
    mode: "split",
    cols: [
      { id: "c0", rowRatio: 0.5, cells: ["a", "b"].map((id) => ({ id, selectedViewId: null, views: [] })) },
      { id: "c1", rowRatio: 0.5, cells: [{ id: "c", selectedViewId: null, views: [] }] },
    ],
    colRatios: [0.5, 0.5],
    activeCellId: activeId,
  };
}

describe("paneByOrdinal", () => {
  it("maps the 1-based reading order to pane ids", () => {
    const l = grid("a");
    expect(paneByOrdinal(l, 1)).toBe("a");
    expect(paneByOrdinal(l, 2)).toBe("b");
    expect(paneByOrdinal(l, 3)).toBe("c");
    expect(paneByOrdinal(l, 4)).toBe("d");
    expect(paneByOrdinal(l, 5)).toBeUndefined();
  });
});

describe("neighborPane", () => {
  it("moves within a column for up/down", () => {
    expect(neighborPane(grid("a"), "down")).toBe("b");
    expect(neighborPane(grid("b"), "up")).toBe("a");
    expect(neighborPane(grid("a"), "up")).toBeUndefined();
    expect(neighborPane(grid("b"), "down")).toBeUndefined();
  });
  it("moves across columns keeping the row for left/right", () => {
    expect(neighborPane(grid("a"), "right")).toBe("c");
    expect(neighborPane(grid("d"), "left")).toBe("b");
    expect(neighborPane(grid("a"), "left")).toBeUndefined();
    expect(neighborPane(grid("c"), "right")).toBeUndefined();
  });
  it("clamps to the nearest row when the target column is shorter", () => {
    // active b is at row1; col1 only has row0 → land on c.
    expect(neighborPane(ragged("b"), "right")).toBe("c");
  });
});

describe("cyclePane", () => {
  it("wraps forward and backward in reading order", () => {
    expect(cyclePane(grid("a"), 1)).toBe("b");
    expect(cyclePane(grid("d"), 1)).toBe("a");
    expect(cyclePane(grid("a"), -1)).toBe("d");
  });
});
