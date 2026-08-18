import { describe, expect, it, vi } from "vitest";
import type { Cell, Layout, OpenTarget, View } from "./types.ts";
import * as ops from "./ops.ts";
import { normalizeStored } from "./migrate.ts";

const target = (name: string): OpenTarget => ({ content: { kind: "terminal", chat: false }, session: name });
const view = (id: string, session = id): View => ({ id, session, content: { kind: "terminal", chat: false }, wrap: null });
const cell = (id: string, views: View[] = [], selected = views[0]?.id || null): Cell => ({ id, views, selectedViewId: selected });
const layout = (cells: Cell[], mode: "split" | "tabs" = "tabs"): Layout => ({
  version: 3,
  mode,
  cols: [{ id: "c0", rowRatio: 0.5, cells }],
  colRatios: [1],
  activeCellId: cells[0].id,
});

describe("cell/view identity", () => {
  it("starts with one real empty cell and no synthetic runtime view", () => {
    for (const l of [ops.freshLayout(), ops.freshTabbedLayout()]) {
      expect(ops.allCells(l)).toHaveLength(1);
      expect(ops.allViews(l)).toEqual([]);
      expect(ops.activeView(l)).toBeUndefined();
    }
  });

  it("selecting a tab changes neither cell identity nor order", () => {
    vi.spyOn(Date, "now").mockReturnValue(10);
    const before = layout([cell("g7", [view("p1"), view("p2")], "p1")]);
    const after = ops.selectView(before, "p2");
    expect(after.activeCellId).toBe("g7");
    expect(after.cols[0].cells[0].id).toBe("g7");
    expect(after.cols[0].cells[0].views.map((v) => v.id)).toEqual(["p1", "p2"]);
    expect(after.cols[0].cells[0].selectedViewId).toBe("p2");
    vi.restoreAllMocks();
  });

  it("selecting a cell does not change its selected view", () => {
    const before = layout([cell("g1", [view("p1")]), cell("g2", [view("p2"), view("p3")], "p3")]);
    const after = ops.selectCell(before, "g2");
    expect(after.activeCellId).toBe("g2");
    expect(after.cols[0].cells[1].selectedViewId).toBe("p3");
  });
});

describe("open/close/reset", () => {
  it("opens into an empty active cell without creating a placeholder tab", () => {
    const after = ops.openActive(ops.freshTabbedLayout(), target("alpha"));
    expect(ops.allViews(after).map((v) => v.session)).toEqual(["alpha"]);
    expect(ops.activeCell(after)?.views).toHaveLength(1);
  });

  it("focuses a duplicate in its existing cell", () => {
    const before = layout([cell("g1", [view("p1", "alpha")]), cell("g2", [view("p2", "beta")])]);
    const after = ops.openInTab(before, target("beta"));
    expect(after.activeCellId).toBe("g2");
    expect(ops.allViews(after)).toHaveLength(2);
  });

  it("closing the selected view falls back to its right neighbor, then left, without usage stamps", () => {
    const before = layout([cell("g1", [view("p1"), view("p2"), view("p3")], "p2")]);
    const right = ops.closeView(before, "p2");
    expect(ops.activeCell(right)?.selectedViewId).toBe("p3");
    const left = ops.closeView(right, "p3");
    expect(ops.activeCell(left)?.selectedViewId).toBe("p1");
  });

  it("closing the selected view returns to the tab shown before it, not the neighbor", () => {
    let l = ops.freshTabbedLayout();
    for (const name of ["alpha", "beta", "gamma"]) l = ops.openInTab(l, target(name));
    l = ops.selectView(l, "p1"); // 例: ミラーのタブを表示している状態
    l = ops.openInTab(l, target("delta")); // そこからリンクを開く（末尾に追加され選択される）
    expect(ops.activeCell(l)?.selectedViewId).toBe("p4");

    const back = ops.closeView(l, "p4");
    expect(ops.activeCell(back)?.selectedViewId).toBe("p1");
    // 戻った先が「今表示中」になるので、次に閉じたときの基準もそこから進む。
    const reopened = ops.openInTab(back, target("epsilon"));
    const again = ops.closeView(reopened, ops.activeView(reopened)!.id);
    expect(ops.activeCell(again)?.selectedViewId).toBe("p1");
  });

  it("a tab retargeted while hidden does not steal the return spot", () => {
    let l = ops.freshTabbedLayout();
    for (const name of ["alpha", "beta"]) l = ops.openInTab(l, target(name));
    l = ops.selectView(l, "p1");
    // 裏のタブの差し替え（ミラーが開いているプラン面を書き換える経路）は「表示した」ではない。
    l = ops.setPaneTarget(l, "p2", { content: { kind: "doc", docTitle: "plan", docContent: "x" } });
    l = ops.openInTab(l, target("gamma"));
    expect(ops.activeCell(ops.closeView(l, "p3"))?.selectedViewId).toBe("p1");
  });

  it("closing a background tab leaves the shown tab alone", () => {
    let l = ops.freshTabbedLayout();
    for (const name of ["alpha", "beta"]) l = ops.openInTab(l, target(name));
    const after = ops.closeView(l, "p1");
    expect(ops.activeCell(after)?.selectedViewId).toBe("p2");
  });

  it("closing the final view leaves a true empty cell", () => {
    const after = ops.closeView(layout([cell("g1", [view("p1")])]), "p1");
    expect(ops.activeCell(after)).toEqual({ id: "g1", views: [], selectedViewId: null });
    expect(ops.allViews(after)).toEqual([]);
  });

  it("removes an empty cell but keeps the final cell", () => {
    const two = layout([cell("g1"), cell("g2", [view("p2")])]);
    expect(ops.allCells(ops.closeCell(two, "g1")).map((c) => c.id)).toEqual(["g2"]);
    const one = ops.closeCell(layout([cell("g1", [view("p1")])]), "g1");
    expect(ops.allCells(one)).toHaveLength(1);
    expect(ops.allViews(one)).toHaveLength(0);
  });

  it("reset preserves the selected profile and drops every saved cell/view", () => {
    for (const mode of ["split", "tabs"] as const) {
      const after = ops.resetLayout(layout([cell("g1", [view("p1")]), cell("g2", [view("p2")])], mode));
      expect(after.mode).toBe(mode);
      expect(ops.allCells(after)).toHaveLength(1);
      expect(ops.allViews(after)).toHaveLength(0);
    }
  });
});

describe("tab drag operations", () => {
  it("reorders in one cell without selecting or reallocating", () => {
    const before = layout([cell("g1", [view("p1"), view("p2"), view("p3")], "p2")]);
    const after = ops.moveTab(before, "p3", "g1", "p1");
    expect(ops.activeCell(after)?.views.map((v) => v.id)).toEqual(["p3", "p1", "p2"]);
    expect(ops.activeCell(after)?.selectedViewId).toBe("p2");
  });

  it("moves to another cell and leaves an empty source after its final view", () => {
    const before = layout([cell("g1", [view("p1")]), cell("g2")]);
    const after = ops.moveTab(before, "p1", "g2");
    expect(ops.cellById(after, "g1")).toEqual({ id: "g1", views: [], selectedViewId: null });
    expect(ops.cellById(after, "g2")?.views.map((v) => v.id)).toEqual(["p1"]);
    expect(after.activeCellId).toBe("g2");
  });

  it("tears the selected tab from its own right edge into a new cell", () => {
    const before = layout([cell("g1", [view("p1"), view("p2")], "p1")]);
    const after = ops.dropSplitTab(before, "p1", "g1", "right");
    expect(after.cols).toHaveLength(2);
    expect(after.cols[0].cells[0].views.map((v) => v.id)).toEqual(["p2"]);
    expect(after.cols[1].cells[0].views.map((v) => v.id)).toEqual(["p1"]);
    expect(ops.viewById(after, "p1")).toBe(before.cols[0].cells[0].views[0]);
  });

  it("creates a lower cell atomically and preserves the runtime id", () => {
    const before = layout([cell("g1", [view("p1")])]);
    const after = ops.dropSplitTab(before, "p1", "g1", "down");
    expect(after.cols[0].cells).toHaveLength(2);
    expect(ops.allViews(after).map((v) => v.id)).toEqual(["p1"]);
    expect(after.cols[0].cells[0].views).toEqual([]);
  });

  it("leaves the source cell on its most recent tab when the shown one is torn out or moved", () => {
    let l = ops.freshTabbedLayout();
    for (const name of ["alpha", "beta", "gamma"]) l = ops.openInTab(l, target(name));
    l = ops.selectView(l, "p1");
    l = ops.selectView(l, "p3");
    l = ops.dropSplitTab(l, "p3", "g0", "right"); // 表示中の p3 を切り離す
    expect(ops.cellById(l, "g0")?.selectedViewId).toBe("p1");

    l = ops.selectView(l, "p2"); // g0 を活性に戻してからもう 1 枚開く
    l = ops.openInTab(l, target("delta")); // p4（末尾・選択される）
    l = ops.selectView(l, "p1");
    l = ops.selectView(l, "p2"); // 直前は p1、表示は p2
    const moved = ops.moveTab(l, "p2", ops.allCells(l)[1].id);
    expect(ops.cellById(moved, "g0")?.selectedViewId).toBe("p1");
  });

  it("enforces three columns and two rows", () => {
    let columns = layout([cell("g1", [view("p1")])]);
    columns = ops.dropSplitTab(columns, "p1", "g1", "right");
    columns = ops.openInTab(columns, target("two"));
    const active = ops.activeView(columns)!;
    columns = ops.dropSplitTab(columns, active.id, columns.activeCellId, "right");
    expect(columns.cols).toHaveLength(3);
    expect(ops.dropSplitTab(columns, "p1", "g1", "right")).toBe(columns);

    const rows = layout([cell("g1", [view("p1"), view("p2")])]);
    const split = ops.dropSplitTab(rows, "p2", "g1", "down");
    expect(ops.dropSplitTab(split, "p1", "g1", "down")).toBe(split);
  });
});

describe("split profile", () => {
  it("keeps at most one view per cell and supports four columns", () => {
    let l = ops.openActive(ops.freshLayout(), target("one"));
    for (let i = 0; i < 3; i++) l = ops.splitRight(l);
    expect(l.cols).toHaveLength(4);
    expect(ops.splitRight(l)).toBe(l);
    expect(ops.allCells(l).every((c) => c.views.length <= 1)).toBe(true);
  });

  it("swaps cell geometry without changing view runtime ids", () => {
    const before = layout([cell("g1", [view("p1")]), cell("g2", [view("p2")])], "split");
    const after = ops.swapPanes(before, "g1", "g2");
    expect(after.cols[0].cells.map((c) => c.id)).toEqual(["g2", "g1"]);
    expect(ops.allViews(after).map((v) => v.id).sort()).toEqual(["p1", "p2"]);
  });
});

describe("layout v3 migration", () => {
  it("converts a legacy tab projection into a stable cell plus ordered views", () => {
    const migrated = normalizeStored({
      mode: "tabs",
      cols: [{ id: "c0", rowRatio: 0.5, panes: [{
        id: "p2", session: "two", content: { kind: "terminal", chat: false }, wrap: null,
        tabs: [{ id: "p1", session: "one", content: { kind: "terminal", chat: false }, wrap: null }],
        tabOrder: ["p1", "p2"],
      }] }],
      colRatios: [1],
      activeId: "p2",
    })!;
    expect(migrated.version).toBe(3);
    expect(migrated.activeCellId).toBe("g0");
    expect(migrated.cols[0].cells[0]).toMatchObject({
      id: "g0",
      selectedViewId: "p2",
      views: [{ id: "p1" }, { id: "p2" }],
    });
  });

  it("removes a legacy synthetic blank but round-trips a v3 empty cell", () => {
    const legacy = normalizeStored({
      mode: "tabs",
      cols: [{ id: "c0", rowRatio: 0.5, panes: [{ id: "p0", session: null, content: { kind: "terminal", chat: false }, wrap: null }] }],
      colRatios: [1], activeId: "p0",
    })!;
    expect(legacy.cols[0].cells[0].views).toEqual([]);
    const empty = ops.freshTabbedLayout();
    expect(normalizeStored(JSON.parse(JSON.stringify(empty)))).toEqual(empty);
  });

  it("round-trips a persisted shared-session view and rejects an unsafe id", () => {
    const shared = ops.singlePaneLayout({ kind: "sharedSession", sharedSessionId: "catalog_abc-123" }, null);
    expect(normalizeStored(JSON.parse(JSON.stringify(shared)))).toEqual(shared);

    const unsafe = JSON.parse(JSON.stringify(shared));
    unsafe.cols[0].cells[0].views[0].content.sharedSessionId = "../../foreign";
    expect(normalizeStored(unsafe)!.cols[0].cells[0].views[0].content).toEqual({ kind: "terminal", chat: false });
  });
});
