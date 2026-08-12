// Pure pane-layout operations (docs/22 P1). Every function is Layout in →
// Layout out, no browser globals, no side effects — the zustand store owns
// persistence/history, the terminal service owns xterm reconciliation. A no-op
// returns the INPUT layout by reference, so callers can `next === cur` to skip
// commits. Ported behavior-for-behavior from the old state.tsx (its comments
// carried over where the reasoning still matters).
import type { Layout, Column, Pane, PaneContent, OpenTarget, PaneView } from "./types.ts";
import { blankPane, paneView } from "./types.ts";

export const MAX_COLS = 4;
export const MAX_TAB_COLS = 3;
export const MAX_TABS = 24;
export const equalRatios = (n: number): number[] => Array(n).fill(1 / n);

export const freshLayout = (): Layout => ({
  mode: "split",
  cols: [{ id: "c0", rowRatio: 0.5, panes: [blankPane("p0")] }],
  colRatios: [1],
  activeId: "p0",
});

export const freshTabbedLayout = (): Layout => ({
  mode: "tabs",
  cols: [{ id: "c0", rowRatio: 0.5, panes: [blankPane("p0")] }],
  colRatios: [1],
  activeId: "p0",
});

/** Close-all returns to one empty pane without changing the selected layout
 * profile. Switching a tabbed layout to `split` here made the settings effect
 * immediately restore the old tabbed arrangement from storage. */
export const resetLayout = (l: Layout): Layout =>
  l.mode === "tabs" ? freshTabbedLayout() : freshLayout();

/** A 1-pane layout showing exactly one descriptor — the pop-out tab's seed
 * (features/panes/popout.ts). Uses the freshLayout ids so a later persist/restore
 * round-trips identically. */
export const singlePaneLayout = (
  content: PaneContent,
  session: string | null,
  wrap: boolean | null = null,
): Layout => ({
  cols: [{ id: "c0", rowRatio: 0.5, panes: [{ id: "p0", session, content, wrap }] }],
  colRatios: [1],
  activeId: "p0",
});

/** Selected views — the established meaning of `allPanes`, retained for rails,
 * keyboard geometry and legacy callers. */
export const allPanes = (l: Layout): Pane[] => l.cols.flatMap((c) => c.panes);
/** Every runtime view, including inactive tabs. */
export const allViews = (l: Layout): PaneView[] =>
  l.cols.flatMap((c) => c.panes.flatMap((p) => [paneView(p), ...(p.tabs || [])]));
export const paneById = (l: Layout, id: string): Pane | undefined =>
  allPanes(l).find((p) => p.id === id);
export const activePane = (l: Layout): Pane | undefined =>
  paneById(l, l.activeId) || allPanes(l)[0];

/** An empty terminal pane — no session, nothing shown. Closing a pane's content
 * blanks it to this; closing an already-blank pane is what actually removes it. */
export const isBlankPane = (p: Pane): boolean => p.content.kind === "terminal" && !p.session;

const tabbed = (l: Layout) => l.mode === "tabs";
const tabCap = (l: Layout) => (tabbed(l) ? MAX_TAB_COLS : MAX_COLS);

const cellViews = (cell: Pane): PaneView[] => [paneView(cell), ...(cell.tabs || [])];

/** Return every view in the visual order saved for this tab cell. Older saved
 * layouts lack tabOrder; their current selected-first order is a safe fallback. */
export function orderedTabViews(cell: Pane): PaneView[] {
  const views = cellViews(cell);
  const byId = new Map(views.map((view) => [view.id, view] as const));
  const out: PaneView[] = [];
  for (const id of cell.tabOrder || []) {
    const view = byId.get(id);
    if (view) { out.push(view); byId.delete(id); }
  }
  for (const view of views) if (byId.has(view.id)) out.push(view);
  return out;
}

function tabCell(selectedId: string, views: PaneView[], order: string[]): Pane {
  const ordered = (() => {
    const byId = new Map(views.map((view) => [view.id, view] as const));
    const out: string[] = [];
    for (const id of order) if (byId.has(id) && !out.includes(id)) out.push(id);
    for (const view of views) if (!out.includes(view.id)) out.push(view.id);
    return out;
  })();
  const selected = views.find((view) => view.id === selectedId);
  if (!selected) throw new Error("selected tab missing from cell");
  return { ...selected, tabs: views.filter((view) => view.id !== selectedId), tabOrder: ordered };
}

function replaceSelected(cell: Pane, view: PaneView): Pane {
  const views = cellViews(cell).map((current) => current.id === view.id ? view : current);
  return tabCell(view.id, views, cell.tabOrder || orderedTabViews(cell).map((current) => current.id));
}

function withoutTab(cell: Pane, id: string, alloc: ReturnType<typeof idAlloc>): Pane {
  const views = orderedTabViews(cell).filter((view) => view.id !== id);
  if (views.length === 0) {
    const blank = blankPane(alloc.nextPane());
    return { ...blank, tabs: [], tabOrder: [blank.id] };
  }
  const selectedId = views.some((view) => view.id === cell.id) ? cell.id : views[0].id;
  return tabCell(selectedId, views, views.map((view) => view.id));
}

/** Make a tab selected in its current cell, preserving all runtime identities. */
export function selectTab(l: Layout, id: string): Layout {
  if (!tabbed(l)) return setActive(l, id);
  for (const col of l.cols) {
    for (const cell of col.panes) {
      if (cell.id === id) {
        const current = tabCell(id, cellViews(cell).map((view) => view.id === id ? { ...view, lastUsedAt: Date.now() } : view), cell.tabOrder || []);
        return { ...l, cols: mapPanes(l, (p) => (p === cell ? current : p)), activeId: id };
      }
      const view = cell.tabs?.find((t) => t.id === id);
      if (!view) continue;
      const cols = l.cols.map((c) => ({
        ...c,
        panes: c.panes.map((p) => (p === cell ? replaceSelected(p, { ...view, lastUsedAt: Date.now() }) : p)),
      }));
      return { ...l, cols, activeId: id };
    }
  }
  return l;
}

/** Id allocator: next pane/column ids past the current maxima. Scanned per call
 * (≤8 panes) instead of stored counters, so history-restored layouts can never
 * race a stale counter into a duplicate id. */
export function idAlloc(l: Layout): { nextPane(): string; nextCol(): string } {
  let pMax = 0;
  let cMax = 0;
  for (const c of l.cols) {
    const cn = parseInt(String(c.id).slice(1), 10);
    if (!Number.isNaN(cn)) cMax = Math.max(cMax, cn);
    for (const p of c.panes) {
      for (const view of [paneView(p), ...(p.tabs || [])]) {
      const pn = parseInt(String(view.id).slice(1), 10);
      if (!Number.isNaN(pn)) pMax = Math.max(pMax, pn);
      }
    }
  }
  return {
    nextPane: () => `p${++pMax}`,
    nextCol: () => `c${++cMax}`,
  };
}

/** True if pane already displays exactly the target (same kind + identity).
 * Used to avoid showing one thing in two panes. A terminal target without a
 * session (bare "show the terminal view") never matches — it isn't a navigation
 * to an identifiable thing. Mirrors the old shows(). */
export function sameTarget(pane: Pane, target: OpenTarget): boolean {
  const c = pane.content;
  const t = target.content;
  if (c.kind !== t.kind) return false;
  switch (t.kind) {
    case "terminal":
      return target.session != null && pane.session === target.session;
    case "file":
      return c.kind === "file" && c.filePath === t.filePath && c.targetLine === t.targetLine && c.targetColumn === t.targetColumn;
    case "read":
      return c.kind === "read" && c.filePath === t.filePath;
    case "scm":
      return c.kind === "scm" && c.scmRepo === t.scmRepo && c.scmPath === t.scmPath;
    case "changes":
      return c.kind === "changes" && c.scmRepo === t.scmRepo;
    case "commit":
      return c.kind === "commit" && c.scmRepo === t.scmRepo && c.scmPath === t.scmPath && c.commitSha === t.commitSha;
    case "wtdiff":
      return c.kind === "wtdiff" && c.scmRepo === t.scmRepo && c.filePath === t.filePath;
    case "doc":
      return c.kind === "doc" && c.docTitle === t.docTitle;
    case "diff":
      return c.kind === "diff" && c.docTitle === t.docTitle && c.diffEdits === t.diffEdits;
    case "chat":
      // A conversation is identified by its id; a not-yet-created draft by its
      // assistant — so "open in a split" focuses an existing chat instead of
      // duplicating it.
      if (t.conversationId) return c.kind === "chat" && c.conversationId === t.conversationId;
      if (t.draftAssistantId) return c.kind === "chat" && c.draftAssistantId === t.draftAssistantId;
      return false;
    case "browser":
      return c.kind === "browser" && c.port === t.port && c.path === t.path;
    case "browserAttach":
      return c.kind === "browserAttach" && c.attachmentId === t.attachmentId;
  }
}

/** Apply a target onto one pane: replace its content; bind the session when the
 * target names one (undefined = keep — the "switch back to terminal" case). */
function applyTarget(p: Pane, target: OpenTarget): Pane {
  return {
    ...p,
    content: target.content,
    session: target.session !== undefined ? target.session : p.session,
  };
}

const mapPanes = (l: Layout, fn: (p: Pane) => Pane): Column[] =>
  l.cols.map((c) => ({ ...c, panes: c.panes.map(fn) }));

/** openActive opens the target in the active pane. When split and ANOTHER pane
 * already shows exactly that target, patching would duplicate it — instead we
 * just focus that pane where it sits (no swap into the active side), so a click
 * on an already-open session activates its pane rather than shuffling panes
 * around. For a terminal target the incoming chat intent still wins (clicking a
 * session open as chat elsewhere flips that pane to its terminal), then focuses. */
export function openActive(l: Layout, target: OpenTarget): Layout {
  if (tabbed(l)) return openInTab(l, target);
  const active = activePane(l);
  if (!active) return l;
  const other = allPanes(l).find((p) => p.id !== l.activeId && sameTarget(p, target));
  if (other) {
    const chat =
      target.content.kind === "terminal" && other.content.kind === "terminal"
        ? target.content.chat
        : undefined;
    const cols =
      chat !== undefined
        ? mapPanes(l, (p) =>
            p.id === other.id && p.content.kind === "terminal"
              ? { ...p, content: { ...p.content, chat } }
              : p,
          )
        : l.cols;
    return { ...l, cols, activeId: other.id };
  }
  const cols = mapPanes(l, (p) => (p.id === l.activeId ? applyTarget(p, target) : p));
  return { ...l, cols };
}

/** Tab profile navigation always accumulates a view in the active cell. Exact
 * duplicates focus the existing tab; at capacity the oldest non-active view is
 * replaced (the store's dirty guard still protects an editor buffer). */
export function openInTab(l: Layout, target: OpenTarget): Layout {
  const dup = allViews(l).find((p) => sameTarget(p as Pane, target));
  if (dup) return selectTab(l, dup.id);
  const active = activePane(l);
  if (!active) return l;
  const make = (id: string): PaneView => ({ ...paneView(blankPane(id)), ...applyTarget(blankPane(id), target), lastUsedAt: Date.now() });
  const alloc = idAlloc(l);
  const view = make(alloc.nextPane());
  const total = allViews(l).length;
  if (isBlankPane(active) && !(active.tabs?.length)) {
    const selected = tabCell(view.id, [view], [view.id]);
    return { ...l, cols: mapPanes(l, (p) => (p.id === active.id ? selected : p)), activeId: view.id };
  }
  if (total < MAX_TABS) {
    const previous = orderedTabViews(active);
    const selected = tabCell(view.id, [...previous, view], [...previous.map((current) => current.id), view.id]);
    return { ...l, cols: mapPanes(l, (p) => (p.id === active.id ? selected : p)), activeId: view.id };
  }
  const victim = allViews(l)
    .filter((p) => p.id !== l.activeId)
    .sort((a, b) => (a.lastUsedAt || 0) - (b.lastUsedAt || 0))[0];
  if (!victim) return l;
  return replaceView(l, victim.id, view);
}

function replaceView(l: Layout, oldId: string, next: PaneView): Layout {
  let replaced = false;
  const cols = l.cols.map((c) => ({
    ...c,
    panes: c.panes.map((p) => {
      if (p.id === oldId) {
        replaced = true;
        const views = cellViews(p).map((view) => view.id === oldId ? next : view);
        return tabCell(next.id, views, (p.tabOrder || orderedTabViews(p).map((view) => view.id)).map((id) => id === oldId ? next.id : id));
      }
      if (!p.tabs?.some((t) => t.id === oldId)) return p;
      replaced = true;
      const views = cellViews(p).map((view) => view.id === oldId ? next : view);
      return tabCell(p.id, views, (p.tabOrder || orderedTabViews(p).map((view) => view.id)).map((id) => id === oldId ? next.id : id));
    }),
  }));
  return replaced ? selectTab({ ...l, cols, activeId: l.activeId }, next.id) : l;
}

export interface OpenInNewOpts {
  /** Phone: no extra columns — grow the active column downward (max 2 panes). */
  mobile?: boolean;
  /** Skip the dedup/fill-blank shortcuts (an explicit "新しいペインで開く"). */
  force?: boolean;
}

/** openInNew opens the target in a fresh pane (made active), in a single new
 * layout. Growth order: focus an existing pane already showing the target →
 * fill a blank pane → add a right column (≤4) → split a single-pane column →
 * reuse a non-active pane once all 8 slots are used (preserving the source pane). */
export function openInNew(l: Layout, target: OpenTarget, opts: OpenInNewOpts = {}): Layout {
  if (tabbed(l)) return openInTab(l, target);
  const alloc = idAlloc(l);
  if (!opts.force) {
    const dup = allPanes(l).find((p) => sameTarget(p, target));
    if (dup) return dup.id === l.activeId ? l : { ...l, activeId: dup.id };
    // Prefer filling a vacant pane over growing the layout. Favor the active
    // pane when it's the blank one, else the first blank in layout order.
    const all = allPanes(l);
    const blank = all.find((p) => p.id === l.activeId && isBlankPane(p)) || all.find(isBlankPane);
    if (blank) {
      const cols = mapPanes(l, (p) =>
        p.id === blank.id ? applyTarget(blankPane(blank.id), target) : p,
      );
      return { ...l, cols, activeId: blank.id };
    }
  }
  const fresh = (id: string) => applyTarget(blankPane(id), target);
  const replacePane = (pane: Pane): Layout => ({
    ...l,
    cols: mapPanes(l, (p) => (p.id === pane.id ? fresh(pane.id) : p)),
    activeId: pane.id,
  });
  const splitColOf = (col: Column): Layout => {
    const id = alloc.nextPane();
    const cols = l.cols.map((c) =>
      c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, fresh(id)] } : c,
    );
    return { ...l, cols, activeId: id };
  };

  if (opts.mobile) {
    const col = l.cols.find((c) => c.panes.some((p) => p.id === l.activeId)) || l.cols[0];
    if (col && col.panes.length < 2) return splitColOf(col);
    if (opts.force) {
      // An explicit separate-pane open must not replace the pane containing the clicked
      // link. At the phone's two-pane cap, reuse the other row instead.
      const other = allPanes(l).find((p) => p.id !== l.activeId);
      if (other) return replacePane(other);
    }
    return openActive(l, target);
  }

  if (l.cols.length < MAX_COLS) {
    const id = alloc.nextPane();
    const cols = [...l.cols, { id: alloc.nextCol(), rowRatio: 0.5, panes: [fresh(id)] }];
    return { ...l, cols, colRatios: equalRatios(cols.length), activeId: id };
  }

  const activeCol = l.cols.find((c) => c.panes.some((p) => p.id === l.activeId));
  const targetCol =
    activeCol && activeCol.panes.length < 2 ? activeCol : l.cols.find((c) => c.panes.length < 2);
  if (targetCol) return splitColOf(targetCol);

  // All 8 slots full — overwrite the last pane (bottom of the rightmost column).
  const panes = allPanes(l);
  const replacement = [...panes].reverse().find((p) => p.id !== l.activeId) || panes[panes.length - 1];
  return replacement ? replacePane(replacement) : l;
}

/** Replace one pane's content by id (not "active") — e.g. a chat draft pane
 * promoting to a real conversation while in the background. */
export function setPaneTarget(l: Layout, paneId: string, target: OpenTarget): Layout {
  if (tabbed(l)) {
    const p = paneById(l, paneId);
    return p ? replaceView(l, paneId, { ...applyTarget(p, target), lastUsedAt: Date.now() }) : l;
  }
  if (!paneById(l, paneId)) return l;
  return { ...l, cols: mapPanes(l, (p) => (p.id === paneId ? applyTarget(p, target) : p)) };
}

export function setPaneWrap(l: Layout, paneId: string, wrap: boolean | null): Layout {
  if (tabbed(l)) {
    const p = paneById(l, paneId);
    return p ? replaceView(l, paneId, { ...paneView(p), wrap }) : l;
  }
  if (!paneById(l, paneId)) return l;
  return { ...l, cols: mapPanes(l, (p) => (p.id === paneId ? { ...p, wrap } : p)) };
}

/** Remove the panes matching `pred`, collapsing emptied splits/columns and
 * re-equalizing widths. Returns a fresh single-blank layout when nothing is left. */
function removePanes(l: Layout, pred: (p: Pane) => boolean): Layout {
  const cols = l.cols
    .map((c) => {
      const panes = c.panes.filter((p) => !pred(p));
      return panes.length === c.panes.length ? c : { ...c, rowRatio: 0.5, panes };
    })
    .filter((c) => c.panes.length > 0);
  const remaining = cols.flatMap((c) => c.panes);
  if (remaining.length === 0) return freshLayout();
  const activeId = remaining.some((p) => p.id === l.activeId) ? l.activeId : remaining[0].id;
  const colRatios = cols.length === l.cols.length ? l.colRatios : equalRatios(cols.length);
  return { ...l, cols, colRatios, activeId };
}

/** closePane closes in TWO steps so a split never collapses on the first close:
 * a pane still holding something is cleared to a blank terminal IN PLACE (same
 * id, split untouched); an already-blank pane is actually removed (its column
 * collapses, widths re-equalize). removeOutright skips step 1. The very last
 * pane can't be removed — it stays as the base blank terminal. */
export function closePane(l: Layout, paneId: string, removeOutright = false): Layout {
  if (tabbed(l)) return closeTab(l, paneId, removeOutright);
  const target = paneById(l, paneId);
  if (!target) return l;
  if (!removeOutright && !isBlankPane(target)) {
    const cols = mapPanes(l, (p) => (p.id === paneId ? blankPane(paneId) : p));
    return { ...l, cols, activeId: paneId };
  }
  return removePanes(l, (p) => p.id === paneId);
}

/** Close one tab directly. The last tab becomes an empty cell; removing that
 * cell is a deliberate second action, matching the approved tab-layout UX. */
export function closeTab(l: Layout, id: string, removeCell = false): Layout {
  if (!tabbed(l)) return closePane(l, id, removeCell);
  for (const col of l.cols) for (const cell of col.panes) {
    const views = orderedTabViews(cell);
    if (!views.some((v) => v.id === id)) continue;
    if (removeCell && views.length === 1) return removePanes(l, (p) => p === cell);
    if (id !== cell.id) {
      const remaining = views.filter((view) => view.id !== id);
      return { ...l, cols: mapPanes(l, (p) => (p === cell ? tabCell(cell.id, remaining, remaining.map((view) => view.id)) : p)) };
    }
    const remaining = views.filter((v) => v.id !== id);
    if (remaining.length === 0) {
      const blank = blankPane(cell.id);
      blank.tabs = [];
      blank.tabOrder = [blank.id];
      return { ...l, cols: mapPanes(l, (p) => (p === cell ? blank : p)), activeId: blank.id };
    }
    const closedAt = views.findIndex((view) => view.id === id);
    const chosen = remaining[Math.min(closedAt, remaining.length - 1)];
    const selected = tabCell(chosen.id, remaining, remaining.map((view) => view.id));
    return { ...l, cols: mapPanes(l, (p) => (p === cell ? selected : p)), activeId: selected.id };
  }
  return l;
}

/** Move a view to another tab cell without allocating an id. The source keeps
 * an empty cell when its final tab leaves; geometry is intentionally stable. */
export function moveTab(l: Layout, id: string, targetId: string, beforeId?: string): Layout {
  if (!tabbed(l)) return l;
  let source: Pane | undefined;
  let view: PaneView | undefined;
  let target: Pane | undefined;
  for (const cell of allPanes(l)) {
    const found = cellViews(cell).find((v) => v.id === id);
    if (found) { source = cell; view = found; }
    if (cell.id === targetId) target = cell;
  }
  if (!source || !view || !target) return l;
  const alloc = idAlloc(l);
  if (source === target) {
    if (beforeId === id) return l;
    const kept = orderedTabViews(source).filter((current) => current.id !== id);
    const index = beforeId ? kept.findIndex((current) => current.id === beforeId) : kept.length;
    kept.splice(index < 0 ? kept.length : index, 0, view);
    return { ...l, cols: mapPanes(l, (p) => (p === source ? tabCell(source.id, kept, kept.map((current) => current.id)) : p)) };
  }
  // A visual cell projects its selected view, so once its last view leaves it
  // needs a genuinely new blank-view identity. Reusing the moved id would make
  // terminal/browser registries see two owners for the same runtime resource.
  const sourceNext = withoutTab(source, id, alloc);
  const targetViews = orderedTabViews(target);
  const insertAt = beforeId ? targetViews.findIndex((current) => current.id === beforeId) : targetViews.length;
  targetViews.splice(insertAt < 0 ? targetViews.length : insertAt, 0, { ...view, lastUsedAt: Date.now() });
  const targetNext = tabCell(view.id, targetViews, targetViews.map((current) => current.id));
  const cols = mapPanes(l, (p) => p === source ? sourceNext : p === target ? targetNext : p);
  return { ...l, cols, activeId: view.id };
}

/** Tear one tab out into a new visual cell. The target edge decides whether
 * the cell becomes a new right column or a row beneath the target cell; the
 * tab itself keeps its runtime id. */
export function dropSplitTab(l: Layout, id: string, refId: string, dir: "right" | "down"): Layout {
  if (!tabbed(l) || id === refId) return l;
  let source: Pane | undefined;
  let view: PaneView | undefined;
  let refCol: Column | undefined;
  for (const col of l.cols) for (const cell of col.panes) {
    if (cell.id === refId) refCol = col;
    const found = [paneView(cell), ...(cell.tabs || [])].find((v) => v.id === id);
    if (found) { source = cell; view = found; }
  }
  if (!source || !view || !refCol) return l;
  if (dir === "right" && l.cols.length >= MAX_TAB_COLS) return l;
  if (dir === "down" && refCol.panes.length >= 2 && source.id !== refId) return l;

  const alloc = idAlloc(l);
  const sourceNext = withoutTab(source, id, alloc);
  const without = l.cols.map((col) => ({
    ...col,
    panes: col.panes.map((cell) => (cell === source ? sourceNext : cell)),
  }));
  if (dir === "right") {
    const cols = [...without, { id: alloc.nextCol(), rowRatio: 0.5, panes: [tabCell(view.id, [view], [view.id])] }];
    return { ...l, cols, colRatios: equalRatios(cols.length), activeId: view.id };
  }
  const targetCol = without.find((col) => col.id === refCol!.id);
  if (!targetCol || targetCol.panes.length >= 2) return l;
  const cols = without.map((col) =>
    col.id === targetCol.id ? { ...col, rowRatio: 0.5, panes: [...col.panes, tabCell(view.id, [view], [view.id])] } : col,
  );
  return { ...l, cols, activeId: view.id };
}

/** closeSessionPanes removes every pane attached to a session (archive / clear /
 * recreate) in one step. No-op when the session isn't shown anywhere. */
export function closeSessionPanes(l: Layout, name: string): Layout {
  if (tabbed(l)) {
    const ids = allViews(l).filter((p) => p.content.kind === "terminal" && p.session === name).map((p) => p.id);
    return ids.reduce((next, id) => closeTab(next, id, true), l);
  }
  const hit = (p: Pane) => p.content.kind === "terminal" && p.session === name;
  if (!allPanes(l).some(hit)) return l;
  return removePanes(l, hit);
}

export function setActive(l: Layout, id: string): Layout {
  if (tabbed(l)) return selectTab(l, id);
  if (l.activeId === id || !paneById(l, id)) return l;
  return { ...l, activeId: id };
}

export function setColRatios(l: Layout, ratios: number[]): Layout {
  // 合計 1 へ正規化してから保存する — ずれた比率をそのまま持つと右端列が
  // はみ出したまま永続化・復元され続ける。
  const sum = ratios.reduce((n, r) => n + r, 0);
  const colRatios = sum > 0 ? ratios.map((r) => r / sum) : equalRatios(ratios.length);
  return { ...l, colRatios };
}

export function setRowRatio(l: Layout, colId: string, r: number): Layout {
  const ratio = Math.min(0.8, Math.max(0.2, r));
  return {
    ...l,
    cols: l.cols.map((c) => (c.id === colId ? { ...c, rowRatio: ratio } : c)),
  };
}

/** splitRight appends a new full-height column (≤ MAX_COLS) holding a fresh
 * blank pane, made active. Column widths reset to equal. No-op at the cap. */
export function splitRight(l: Layout): Layout {
  if (l.cols.length >= tabCap(l)) return l;
  const alloc = idAlloc(l);
  const id = alloc.nextPane();
  const cols = [...l.cols, { id: alloc.nextCol(), rowRatio: 0.5, panes: [blankPane(id)] }];
  return { ...l, cols, colRatios: equalRatios(cols.length), activeId: id };
}

/** splitDown splits the column holding paneId into two rows, adding a fresh
 * blank pane below, made active. No-op if that column already has 2 rows. */
export function splitDown(l: Layout, paneId: string): Layout {
  const col = l.cols.find((c) => c.panes.some((p) => p.id === paneId));
  if (!col || col.panes.length >= 2) return l;
  const alloc = idAlloc(l);
  const id = alloc.nextPane();
  const cols = l.cols.map((c) =>
    c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, blankPane(id)] } : c,
  );
  return { ...l, cols, activeId: id };
}

/** swapPanes exchanges the positions of two complete panes. Their ids and runtime
 * resources move with them; the dragged pane becomes active at its new position. */
export function swapPanes(l: Layout, aId: string, bId: string): Layout {
  if (!aId || !bId || aId === bId) return l;
  const a = paneById(l, aId);
  const b = paneById(l, bId);
  if (!a || !b) return l;
  const cols = mapPanes(l, (p) => (p.id === aId ? b : p.id === bId ? a : p));
  return { ...l, cols, activeId: aId };
}

/** dropSplit MOVES a dragged pane to a new split position (new right column, or
 * a downward split of the pane it was dropped onto). It relocates the SAME pane
 * object keeping its id — a new id would build a fresh xterm + WebGL context
 * and blank the moved terminal (see the paneId contract in types.ts). */
export function dropSplit(l: Layout, srcId: string, refId: string, dir: "right" | "down"): Layout {
  const src = paneById(l, srcId);
  if (!src || srcId === refId) return l;

  // Pull the pane out of its current column; drop a column it leaves empty.
  const without = l.cols
    .map((c) => {
      const panes = c.panes.filter((p) => p.id !== srcId);
      return panes.length === c.panes.length ? c : { ...c, rowRatio: 0.5, panes };
    })
    .filter((c) => c.panes.length > 0);

  if (dir === "right") {
    const alloc = idAlloc(l);
    const cols = without.concat([{ id: alloc.nextCol(), rowRatio: 0.5, panes: [src] }]);
    if (cols.length > tabCap(l)) return l; // the freed origin column may offset this, so re-check
    return { ...l, cols, colRatios: equalRatios(cols.length), activeId: srcId };
  }
  // dir === 'down': add the pane as a second row under the dropped-onto pane's column.
  const col = without.find((c) => c.panes.some((p) => p.id === refId));
  if (!col || col.panes.length >= 2) return l;
  const cols = without.map((c) =>
    c.id === col.id ? { ...c, rowRatio: 0.5, panes: [...c.panes, src] } : c,
  );
  const colRatios = cols.length === l.cols.length ? l.colRatios : equalRatios(cols.length);
  return { ...l, cols, colRatios, activeId: srcId };
}

// Re-export the content type for convenience of ops callers.
export type { OpenTarget, PaneContent };
