// Keyboard pane navigation — pure geometry over the layout grid (docs: keymap
// redesign, P1). The grid is columns (left→right) each with 1–2 rows (top→bottom);
// badges.ts already assigns every pane a stable 1-based reading-order ordinal and its
// {col,row}, so these functions just pick a target pane id. The caller commits with
// useLayoutStore.setActive(id) — content focus follows via the existing active-pane
// effects (TerminalView focusTerm etc.), so nav never touches the DOM. vitest-covered.
import type { Layout } from "./types.ts";
import { paneRows } from "./badges.ts";
import { activeCell, orderedTabViews } from "./ops.ts";

export type Dir = "left" | "right" | "up" | "down";

/** The pane id at 1-based reading-order ordinal N (matches the visible corner chip /
 * rail badge / mini-map colors), or undefined if there is no Nth pane. */
export function paneByOrdinal(layout: Layout, n: number): string | undefined {
  return paneRows(layout).find((r) => r.ordinal === n)?.id;
}

/** The pane id one step in `dir` from the active pane, or undefined at an edge.
 * left/right change columns (keeping the same row when the target column has one),
 * up/down move within the active pane's column. */
export function neighborPane(layout: Layout, dir: Dir): string | undefined {
  const rows = paneRows(layout);
  const active = rows.find((r) => r.id === layout.activeCellId) ?? rows[0];
  if (!active) return undefined;

  if (dir === "up" || dir === "down") {
    const targetRow = active.row + (dir === "down" ? 1 : -1);
    return rows.find((r) => r.col === active.col && r.row === targetRow)?.id;
  }

  const targetCol = active.col + (dir === "right" ? 1 : -1);
  const inCol = rows.filter((r) => r.col === targetCol);
  if (inCol.length === 0) return undefined;
  // Prefer the same row; otherwise clamp to the target column's nearest existing row
  // (e.g. moving from a bottom pane into a single-pane column lands on its one pane).
  const maxRow = Math.max(...inCol.map((r) => r.row));
  const wantRow = Math.min(active.row, maxRow);
  return (inCol.find((r) => r.row === wantRow) ?? inCol[0]).id;
}

/** The pane id `delta` steps from the active pane in reading order, wrapping around.
 * delta +1 = next, -1 = previous. */
export function cyclePane(layout: Layout, delta: number): string | undefined {
  const rows = paneRows(layout);
  if (rows.length === 0) return undefined;
  const idx = rows.findIndex((r) => r.id === layout.activeCellId);
  const cur = idx < 0 ? 0 : idx;
  // 二重 mod: |delta| > rows.length の負方向でも負インデックスにならない。
  return rows[(((cur + delta) % rows.length) + rows.length) % rows.length].id;
}

/** The view (tab) id `delta` steps from the active cell's selected tab, wrapping
 * around. Tabs live inside one cell, so this stays in the active pane — the pane axis
 * is cyclePane's job. Returns undefined when there is nothing to cycle (a cell with
 * 0 or 1 tab, i.e. every cell in split mode), which is what lets the caller leave the
 * key to the terminal. */
export function cycleTab(layout: Layout, delta: number): string | undefined {
  const cell = activeCell(layout);
  if (!cell) return undefined;
  const views = orderedTabViews(cell);
  if (views.length < 2) return undefined;
  const idx = views.findIndex((v) => v.id === cell.selectedViewId);
  const cur = idx < 0 ? 0 : idx;
  return views[(((cur + delta) % views.length) + views.length) % views.length].id;
}
