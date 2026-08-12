import type { Cell, Column, Layout, OpenTarget, Pane, PaneContent, View } from "./types.ts";
import { blankPane, emptyCell } from "./types.ts";

export const MAX_COLS = 4;
export const MAX_TAB_COLS = 3;
export const MAX_TABS = 24;
export const equalRatios = (n: number): number[] => Array(n).fill(1 / n);

const initial = (mode: "split" | "tabs"): Layout => ({
  version: 3,
  mode,
  cols: [{ id: "c0", rowRatio: 0.5, cells: [emptyCell("g0")] }],
  colRatios: [1],
  activeCellId: "g0",
});
export const freshLayout = (): Layout => initial("split");
export const freshTabbedLayout = (): Layout => initial("tabs");
export const resetLayout = (l: Layout): Layout => initial(l.mode === "tabs" ? "tabs" : "split");

export const singlePaneLayout = (
  content: PaneContent,
  session: string | null,
  wrap: boolean | null = null,
): Layout => ({
  version: 3,
  mode: "split",
  cols: [{ id: "c0", rowRatio: 0.5, cells: [{ id: "g0", selectedViewId: "p0", views: [{ id: "p0", session, content, wrap }] }] }],
  colRatios: [1],
  activeCellId: "g0",
});

export const allCells = (l: Layout): Cell[] => l.cols.flatMap((c) => c.cells);
export const allViews = (l: Layout): View[] => allCells(l).flatMap((c) => c.views);
export const cellById = (l: Layout, id: string): Cell | undefined => allCells(l).find((c) => c.id === id);
export const viewById = (l: Layout, id: string): View | undefined => allViews(l).find((v) => v.id === id);
export const activeCell = (l: Layout): Cell | undefined => cellById(l, l.activeCellId) || allCells(l)[0];
export const selectedView = (cell: Cell | undefined): View | undefined =>
  cell?.views.find((v) => v.id === cell.selectedViewId);
export const activeView = (l: Layout): View | undefined => selectedView(activeCell(l));

/** Content-oriented compatibility selectors. They never expose Cell ids. */
export const allPanes = (l: Layout): Pane[] => allCells(l).flatMap((c) => selectedView(c) || []);
export const paneById = (l: Layout, id: string): Pane | undefined => viewById(l, id);
export const activePane = activeView;
export const isBlankPane = (p: Pane | Cell): boolean =>
  "views" in p ? p.views.length === 0 : p.content.kind === "terminal" && !p.session;
export const orderedTabViews = (cell: Cell): View[] => cell.views;

const tabbed = (l: Layout) => l.mode === "tabs";
const mapCells = (l: Layout, fn: (cell: Cell) => Cell): Column[] =>
  l.cols.map((col) => ({ ...col, cells: col.cells.map(fn) }));
const containingCell = (l: Layout, viewId: string): Cell | undefined =>
  allCells(l).find((cell) => cell.views.some((view) => view.id === viewId));
const touch = (view: View): View => ({ ...view, lastUsedAt: Date.now() });

export function idAlloc(l: Layout) {
  let view = 0, cell = 0, col = 0;
  for (const c of l.cols) {
    const cn = parseInt(c.id.replace(/^\D+/, ""), 10);
    if (!Number.isNaN(cn)) col = Math.max(col, cn);
    for (const g of c.cells) {
      const gn = parseInt(g.id.replace(/^\D+/, ""), 10);
      if (!Number.isNaN(gn)) cell = Math.max(cell, gn);
      for (const v of g.views) {
        const vn = parseInt(v.id.replace(/^\D+/, ""), 10);
        if (!Number.isNaN(vn)) view = Math.max(view, vn);
      }
    }
  }
  const nextView = () => `p${++view}`;
  return { nextView, nextPane: nextView, nextCell: () => `g${++cell}`, nextCol: () => `c${++col}` };
}

export function sameTarget(view: View, target: OpenTarget): boolean {
  const c = view.content, t = target.content;
  if (c.kind !== t.kind) return false;
  switch (t.kind) {
    case "terminal": return target.session != null && view.session === target.session;
    case "file": return c.kind === "file" && c.filePath === t.filePath && c.targetLine === t.targetLine && c.targetColumn === t.targetColumn;
    case "read": return c.kind === "read" && c.filePath === t.filePath;
    case "scm": return c.kind === "scm" && c.scmRepo === t.scmRepo && c.scmPath === t.scmPath;
    case "changes": return c.kind === "changes" && c.scmRepo === t.scmRepo;
    case "commit": return c.kind === "commit" && c.scmRepo === t.scmRepo && c.scmPath === t.scmPath && c.commitSha === t.commitSha;
    case "wtdiff": return c.kind === "wtdiff" && c.scmRepo === t.scmRepo && c.filePath === t.filePath && c.diffStaged === t.diffStaged;
    case "doc": return c.kind === "doc" && c.docTitle === t.docTitle;
    case "diff": return c.kind === "diff" && c.docTitle === t.docTitle && c.diffEdits === t.diffEdits;
    case "chat":
      return t.conversationId ? c.kind === "chat" && c.conversationId === t.conversationId
        : !!t.draftAssistantId && c.kind === "chat" && c.draftAssistantId === t.draftAssistantId;
    case "browser": return c.kind === "browser" && c.port === t.port && c.path === t.path;
    case "browserAttach": return c.kind === "browserAttach" && c.attachmentId === t.attachmentId;
  }
}

const applyTarget = (view: View, target: OpenTarget): View => ({
  ...view,
  content: target.content,
  session: target.session !== undefined ? target.session : view.session,
});
const newView = (id: string, target: OpenTarget): View => touch(applyTarget(blankPane(id), target));

export function selectCell(l: Layout, cellId: string): Layout {
  if (l.activeCellId === cellId || !cellById(l, cellId)) return l;
  return { ...l, activeCellId: cellId };
}
export function selectView(l: Layout, viewId: string): Layout {
  const cell = containingCell(l, viewId);
  if (!cell) return l;
  const changed = cell.selectedViewId !== viewId || l.activeCellId !== cell.id;
  if (!changed) return l;
  return {
    ...l,
    cols: mapCells(l, (current) => current === cell
      ? { ...current, selectedViewId: viewId, views: current.views.map((v) => v.id === viewId ? touch(v) : v) }
      : current),
    activeCellId: cell.id,
  };
}
export const selectTab = selectView;
export const setActive = selectCell;

export function openInTab(l: Layout, target: OpenTarget): Layout {
  const duplicate = allViews(l).find((v) => sameTarget(v, target));
  if (duplicate) return selectView(l, duplicate.id);
  const cell = activeCell(l);
  if (!cell) return l;
  const alloc = idAlloc(l);
  const view = newView(alloc.nextView(), target);
  if (allViews(l).length >= MAX_TABS) {
    const victim = allViews(l).filter((v) => v.id !== activeView(l)?.id)
      .sort((a, b) => (a.lastUsedAt || 0) - (b.lastUsedAt || 0))[0];
    if (!victim) return l;
    return replaceView(l, victim.id, view, true);
  }
  return {
    ...l,
    cols: mapCells(l, (current) => current === cell
      ? { ...current, selectedViewId: view.id, views: [...current.views, view] }
      : current),
    activeCellId: cell.id,
  };
}

function replaceView(l: Layout, oldId: string, next: View, select = false): Layout {
  const cell = containingCell(l, oldId);
  if (!cell) return l;
  return {
    ...l,
    cols: mapCells(l, (current) => current === cell ? {
      ...current,
      selectedViewId: select || current.selectedViewId === oldId ? next.id : current.selectedViewId,
      views: current.views.map((v) => v.id === oldId ? next : v),
    } : current),
    activeCellId: select ? cell.id : l.activeCellId,
  };
}

export function openActive(l: Layout, target: OpenTarget): Layout {
  if (tabbed(l)) return openInTab(l, target);
  const duplicate = allViews(l).find((v) => sameTarget(v, target));
  if (duplicate) {
    const cell = containingCell(l, duplicate.id)!;
    const updated = target.content.kind === "terminal" && duplicate.content.kind === "terminal"
      ? replaceView(l, duplicate.id, { ...duplicate, content: { ...duplicate.content, chat: target.content.chat } }) : l;
    return { ...updated, activeCellId: cell.id };
  }
  const cell = activeCell(l);
  if (!cell) return l;
  const alloc = idAlloc(l);
  const current = selectedView(cell);
  const view = current ? applyTarget(current, target) : newView(alloc.nextView(), target);
  return { ...l, cols: mapCells(l, (c) => c === cell ? { ...c, selectedViewId: view.id, views: [view] } : c) };
}

export interface OpenInNewOpts { mobile?: boolean; force?: boolean }
export function openInNew(l: Layout, target: OpenTarget, opts: OpenInNewOpts = {}): Layout {
  if (tabbed(l)) return openInTab(l, target);
  if (!opts.force) {
    const duplicate = allViews(l).find((v) => sameTarget(v, target));
    if (duplicate) return selectView(l, duplicate.id);
    const blank = allCells(l).find((c) => c.id === l.activeCellId && c.views.length === 0) || allCells(l).find((c) => c.views.length === 0);
    if (blank) return openActive({ ...l, activeCellId: blank.id }, target);
  }
  const alloc = idAlloc(l);
  const view = newView(alloc.nextView(), target);
  const cell: Cell = { id: alloc.nextCell(), selectedViewId: view.id, views: [view] };
  if (opts.mobile) {
    const col = l.cols.find((c) => c.cells.some((g) => g.id === l.activeCellId)) || l.cols[0];
    if (col.cells.length < 2) return { ...l, cols: l.cols.map((c) => c === col ? { ...c, rowRatio: 0.5, cells: [...c.cells, cell] } : c), activeCellId: cell.id };
    const other = col.cells.find((c) => c.id !== l.activeCellId);
    return other ? { ...replaceCellViews(l, other.id, [view], view.id), activeCellId: other.id } : l;
  }
  if (l.cols.length < MAX_COLS) {
    const cols = [...l.cols, { id: alloc.nextCol(), rowRatio: 0.5, cells: [cell] }];
    return { ...l, cols, colRatios: equalRatios(cols.length), activeCellId: cell.id };
  }
  const col = l.cols.find((c) => c.cells.length < 2);
  if (col) return { ...l, cols: l.cols.map((c) => c === col ? { ...c, rowRatio: 0.5, cells: [...c.cells, cell] } : c), activeCellId: cell.id };
  const victim = [...allCells(l)].reverse().find((c) => c.id !== l.activeCellId) || allCells(l).at(-1);
  return victim ? { ...replaceCellViews(l, victim.id, [view], view.id), activeCellId: victim.id } : l;
}

const replaceCellViews = (l: Layout, cellId: string, views: View[], selectedViewId: string | null): Layout => ({
  ...l, cols: mapCells(l, (c) => c.id === cellId ? { ...c, views, selectedViewId } : c),
});

export function setPaneTarget(l: Layout, viewId: string, target: OpenTarget): Layout {
  const view = viewById(l, viewId);
  return view ? replaceView(l, viewId, touch(applyTarget(view, target))) : l;
}
export const setViewTarget = setPaneTarget;
export function setPaneWrap(l: Layout, viewId: string, wrap: boolean | null): Layout {
  const view = viewById(l, viewId);
  return view ? replaceView(l, viewId, { ...view, wrap }) : l;
}
export const setViewWrap = setPaneWrap;

function removeCells(l: Layout, ids: Set<string>): Layout {
  const cols = l.cols.map((c) => ({ ...c, cells: c.cells.filter((g) => !ids.has(g.id)) })).filter((c) => c.cells.length);
  if (!cols.length) return resetLayout(l);
  const cells = cols.flatMap((c) => c.cells);
  return {
    ...l, cols,
    colRatios: cols.length === l.cols.length ? l.colRatios : equalRatios(cols.length),
    activeCellId: cells.some((c) => c.id === l.activeCellId) ? l.activeCellId : cells[0].id,
  };
}

export function closeView(l: Layout, viewId: string): Layout {
  const cell = containingCell(l, viewId);
  if (!cell) return l;
  const index = cell.views.findIndex((v) => v.id === viewId);
  const views = cell.views.filter((v) => v.id !== viewId);
  const selectedViewId = cell.selectedViewId === viewId
    ? (views[Math.min(index, views.length - 1)]?.id || null) : cell.selectedViewId;
  return replaceCellViews(l, cell.id, views, selectedViewId);
}
export function closeCell(l: Layout, cellId: string): Layout {
  if (!cellById(l, cellId) || allCells(l).length === 1) return replaceCellViews(l, cellId, [], null);
  return removeCells(l, new Set([cellId]));
}
export function closeTab(l: Layout, viewId: string, removeCell = false): Layout {
  const cell = containingCell(l, viewId);
  if (!cell) return l;
  return removeCell && cell.views.length === 1 ? closeCell(l, cell.id) : closeView(l, viewId);
}
/** Legacy UI entry: a View id closes a view; a Cell id closes the cell. */
export function closePane(l: Layout, id: string, removeOutright = false): Layout {
  const cell = cellById(l, id);
  if (cell) {
    if (tabbed(l) || removeOutright || cell.views.length === 0) return closeCell(l, id);
    return replaceCellViews(l, id, [], null);
  }
  const owner = containingCell(l, id);
  if (!owner) return l;
  if (!tabbed(l) && removeOutright) return closeCell(l, owner.id);
  return closeView(l, id);
}

export function moveTab(l: Layout, viewId: string, targetCellId: string, beforeViewId?: string): Layout {
  if (!tabbed(l)) return l;
  const source = containingCell(l, viewId), target = cellById(l, targetCellId), view = viewById(l, viewId);
  if (!source || !target || !view) return l;
  if (source === target) {
    if (beforeViewId === viewId) return l;
    const views = source.views.filter((v) => v.id !== viewId);
    const at = beforeViewId ? views.findIndex((v) => v.id === beforeViewId) : views.length;
    views.splice(at < 0 ? views.length : at, 0, view);
    if (views.every((v, i) => v === source.views[i])) return l;
    return replaceCellViews(l, source.id, views, source.selectedViewId);
  }
  const sourceViews = source.views.filter((v) => v.id !== viewId);
  const sourceSelected = source.selectedViewId === viewId
    ? sourceViews[Math.min(source.views.indexOf(view), sourceViews.length - 1)]?.id || null : source.selectedViewId;
  const targetViews = [...target.views];
  const at = beforeViewId ? targetViews.findIndex((v) => v.id === beforeViewId) : targetViews.length;
  targetViews.splice(at < 0 ? targetViews.length : at, 0, touch(view));
  return {
    ...l,
    cols: mapCells(l, (c) => c === source ? { ...c, views: sourceViews, selectedViewId: sourceSelected }
      : c === target ? { ...c, views: targetViews, selectedViewId: viewId } : c),
    activeCellId: target.id,
  };
}

export function dropSplitTab(l: Layout, viewId: string, refCellId: string, dir: "right" | "down"): Layout {
  if (!tabbed(l)) return l;
  const source = containingCell(l, viewId), ref = cellById(l, refCellId), view = viewById(l, viewId);
  const refCol = l.cols.find((c) => c.cells.some((g) => g.id === refCellId));
  if (!source || !ref || !view || !refCol) return l;
  if (dir === "right" && l.cols.length >= MAX_TAB_COLS) return l;
  if (dir === "down" && refCol.cells.length >= 2) return l;
  const alloc = idAlloc(l);
  const sourceViews = source.views.filter((v) => v.id !== viewId);
  const sourceSelected = source.selectedViewId === viewId ? sourceViews[0]?.id || null : source.selectedViewId;
  const next = { ...l, cols: mapCells(l, (c) => c === source ? { ...c, views: sourceViews, selectedViewId: sourceSelected } : c) };
  const cell: Cell = { id: alloc.nextCell(), selectedViewId: viewId, views: [view] };
  if (dir === "right") {
    const index = next.cols.findIndex((c) => c.id === refCol.id);
    const cols = [...next.cols];
    cols.splice(index + 1, 0, { id: alloc.nextCol(), rowRatio: 0.5, cells: [cell] });
    return { ...next, cols, colRatios: equalRatios(cols.length), activeCellId: cell.id };
  }
  return { ...next, cols: next.cols.map((c) => c.id === refCol.id ? { ...c, rowRatio: 0.5, cells: [...c.cells, cell] } : c), activeCellId: cell.id };
}

export function closeSessionPanes(l: Layout, name: string): Layout {
  const ids = allViews(l).filter((v) => v.content.kind === "terminal" && v.session === name).map((v) => v.id);
  return ids.reduce((next, id) => closeView(next, id), l);
}
export function setColRatios(l: Layout, ratios: number[]): Layout {
  const sum = ratios.reduce((n, r) => n + r, 0);
  return { ...l, colRatios: sum > 0 ? ratios.map((r) => r / sum) : equalRatios(ratios.length) };
}
export function setRowRatio(l: Layout, colId: string, r: number): Layout {
  return { ...l, cols: l.cols.map((c) => c.id === colId ? { ...c, rowRatio: Math.min(0.8, Math.max(0.2, r)) } : c) };
}

export function splitRight(l: Layout): Layout {
  const cap = tabbed(l) ? MAX_TAB_COLS : MAX_COLS;
  if (l.cols.length >= cap) return l;
  const alloc = idAlloc(l), cell = emptyCell(alloc.nextCell());
  const cols = [...l.cols, { id: alloc.nextCol(), rowRatio: 0.5, cells: [cell] }];
  return { ...l, cols, colRatios: equalRatios(cols.length), activeCellId: cell.id };
}
export function splitDown(l: Layout, cellId: string): Layout {
  const col = l.cols.find((c) => c.cells.some((g) => g.id === cellId));
  if (!col || col.cells.length >= 2) return l;
  const cell = emptyCell(idAlloc(l).nextCell());
  return { ...l, cols: l.cols.map((c) => c === col ? { ...c, rowRatio: 0.5, cells: [...c.cells, cell] } : c), activeCellId: cell.id };
}
export function swapPanes(l: Layout, aId: string, bId: string): Layout {
  if (aId === bId) return l;
  const a = cellById(l, aId), b = cellById(l, bId);
  if (!a || !b) return l;
  return { ...l, cols: mapCells(l, (c) => c === a ? b : c === b ? a : c), activeCellId: a.id };
}
export function dropSplit(l: Layout, srcId: string, refId: string, dir: "right" | "down"): Layout {
  if (srcId === refId) return l;
  const source = cellById(l, srcId), refCol = l.cols.find((c) => c.cells.some((g) => g.id === refId));
  if (!source || !refCol) return l;
  if (dir === "right" && l.cols.length >= (tabbed(l) ? MAX_TAB_COLS : MAX_COLS)) return l;
  if (dir === "down" && refCol.cells.length >= 2 && !refCol.cells.some((cell) => cell.id === srcId)) return l;
  const without = removeCells(l, new Set([srcId]));
  if (dir === "right") {
    const alloc = idAlloc(without), index = without.cols.findIndex((c) => c.id === refCol.id), cols = [...without.cols];
    cols.splice(index + 1, 0, { id: alloc.nextCol(), rowRatio: 0.5, cells: [source] });
    return { ...without, cols, colRatios: equalRatios(cols.length), activeCellId: source.id };
  }
  return { ...without, cols: without.cols.map((c) => c.id === refCol.id ? { ...c, rowRatio: 0.5, cells: [...c.cells, source] } : c), activeCellId: source.id };
}
