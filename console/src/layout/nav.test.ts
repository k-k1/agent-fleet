import { describe, it, expect } from "vitest";
import type { Layout } from "./types.ts";
import type { View } from "./types.ts";
import { paneByOrdinal, neighborPane, cyclePane, cycleTab } from "./nav.ts";

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

// Tabs mode: cell "a" owns three tabs, cell "b" one — cycling never leaves the
// active cell (the pane axis is cyclePane's job).
const view = (id: string): View => ({ id, session: null, content: { kind: "terminal", chat: false }, wrap: null });
function tabs(activeId: string, selected: string | null): Layout {
  return {
    version: 3,
    mode: "tabs",
    cols: [
      {
        id: "c0",
        rowRatio: 0.5,
        cells: [
          { id: "a", selectedViewId: selected, views: [view("v1"), view("v2"), view("v3")] },
          { id: "b", selectedViewId: "v9", views: [view("v9")] },
        ],
      },
    ],
    colRatios: [1],
    activeCellId: activeId,
  };
}

describe("cycleTab", () => {
  it("wraps forward and backward inside the active cell", () => {
    expect(cycleTab(tabs("a", "v1"), 1)).toBe("v2");
    expect(cycleTab(tabs("a", "v3"), 1)).toBe("v1");
    expect(cycleTab(tabs("a", "v1"), -1)).toBe("v3");
  });
  it("gives up when there is nothing to cycle, so the key falls through to the terminal", () => {
    expect(cycleTab(tabs("b", "v1"), 1)).toBeUndefined(); // single-tab cell
    expect(cycleTab(grid("a"), 1)).toBeUndefined(); // split mode: cells hold no tabs
  });
  it("starts from the first tab when the cell has no selection", () => {
    expect(cycleTab(tabs("a", null), 1)).toBe("v2");
  });
});
