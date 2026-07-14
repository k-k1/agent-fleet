// Pure pane-layout operations (docs/22 P1). Every function is Layout in →
// Layout out, no browser globals, no side effects — the zustand store owns
// persistence/history, the terminal service owns xterm reconciliation. A no-op
// returns the INPUT layout by reference, so callers can `next === cur` to skip
// commits. Ported behavior-for-behavior from the old state.tsx (its comments
// carried over where the reasoning still matters).
import type { Layout, Column, Pane, PaneContent, OpenTarget } from "./types.ts";
import { blankPane } from "./types.ts";

export const MAX_COLS = 4;
export const equalRatios = (n: number): number[] => Array(n).fill(1 / n);

export const freshLayout = (): Layout => ({
  cols: [{ id: "c0", rowRatio: 0.5, panes: [blankPane("p0")] }],
  colRatios: [1],
  activeId: "p0",
});

export const allPanes = (l: Layout): Pane[] => l.cols.flatMap((c) => c.panes);
export const paneById = (l: Layout, id: string): Pane | undefined =>
  allPanes(l).find((p) => p.id === id);
export const activePane = (l: Layout): Pane | undefined =>
  paneById(l, l.activeId) || allPanes(l)[0];

/** An empty terminal pane — no session, nothing shown. Closing a pane's content
 * blanks it to this; closing an already-blank pane is what actually removes it. */
export const isBlankPane = (p: Pane): boolean => p.content.kind === "terminal" && !p.session;

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
      const pn = parseInt(String(p.id).slice(1), 10);
      if (!Number.isNaN(pn)) pMax = Math.max(pMax, pn);
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
  if (!paneById(l, paneId)) return l;
  return { ...l, cols: mapPanes(l, (p) => (p.id === paneId ? applyTarget(p, target) : p)) };
}

export function setPaneWrap(l: Layout, paneId: string, wrap: boolean | null): Layout {
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
  const target = paneById(l, paneId);
  if (!target) return l;
  if (!removeOutright && !isBlankPane(target)) {
    const cols = mapPanes(l, (p) => (p.id === paneId ? blankPane(paneId) : p));
    return { ...l, cols, activeId: paneId };
  }
  return removePanes(l, (p) => p.id === paneId);
}

/** closeSessionPanes removes every pane attached to a session (archive / clear /
 * recreate) in one step. No-op when the session isn't shown anywhere. */
export function closeSessionPanes(l: Layout, name: string): Layout {
  const hit = (p: Pane) => p.content.kind === "terminal" && p.session === name;
  if (!allPanes(l).some(hit)) return l;
  return removePanes(l, hit);
}

export function setActive(l: Layout, id: string): Layout {
  if (l.activeId === id || !paneById(l, id)) return l;
  return { ...l, activeId: id };
}

export function setColRatios(l: Layout, ratios: number[]): Layout {
  return { ...l, colRatios: ratios };
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
  if (l.cols.length >= MAX_COLS) return l;
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

/** swapPanes exchanges the payloads of two panes (ids stay in place — the
 * paneId contract), made by a drag-and-drop. The drop target becomes active. */
export function swapPanes(l: Layout, aId: string, bId: string): Layout {
  if (!aId || !bId || aId === bId) return l;
  const a = paneById(l, aId);
  const b = paneById(l, bId);
  if (!a || !b) return l;
  const cols = mapPanes(l, (p) =>
    p.id === aId ? { ...b, id: aId } : p.id === bId ? { ...a, id: bId } : p,
  );
  return { ...l, cols, activeId: bId };
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
    if (cols.length > MAX_COLS) return l; // the freed origin column may offset this, so re-check
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
