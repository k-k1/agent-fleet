// Undoing an edit that reached CodeView's read-only contentEditable grid (an IME composition,
// which cannot be cancelled). The grid is put back row by row, in place, so the focused element
// survives and the reader's caret/selection can be returned to where the edit began.

/** A selection end as a line number and a character offset into that line's text. */
export interface GridPoint {
  ln: number;
  off: number;
}

export interface GridSelection {
  anchor: GridPoint;
  focus: GridPoint;
}

function pointOf(grid: HTMLElement, node: Node, offset: number): GridPoint | null {
  const el = node.nodeType === Node.ELEMENT_NODE ? (node as Element) : node.parentElement;
  const code = el?.closest<HTMLElement>(".cl-code");
  if (!code || !grid.contains(code)) return null;
  const range = document.createRange();
  range.setStart(code, 0);
  range.setEnd(node, offset);
  return { ln: Number(code.dataset.ln), off: range.toString().length };
}

/** The grid's current selection, or null when it is not inside the grid's code cells. */
export function captureGridSelection(grid: HTMLElement): GridSelection | null {
  const sel = document.getSelection();
  if (!sel || !sel.anchorNode || !sel.focusNode) return null;
  const anchor = pointOf(grid, sel.anchorNode, sel.anchorOffset);
  const focus = pointOf(grid, sel.focusNode, sel.focusOffset);
  return anchor && focus ? { anchor, focus } : null;
}

function locate(grid: HTMLElement, p: GridPoint): [Node, number] | null {
  const code = grid.querySelector<HTMLElement>(`.cl-code[data-ln="${p.ln}"]`);
  if (!code) return null;
  const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
  let left = p.off;
  let last: Text | null = null;
  for (let t = walker.nextNode() as Text | null; t; t = walker.nextNode() as Text | null) {
    if (left <= t.length) return [t, left];
    left -= t.length;
    last = t;
  }
  // The line is shorter than the offset (or empty): clamp to its end.
  return last ? [last, last.length] : [code, 0];
}

export function applyGridSelection(grid: HTMLElement, s: GridSelection): void {
  const a = locate(grid, s.anchor);
  const f = locate(grid, s.focus);
  if (a && f) document.getSelection()?.setBaseAndExtent(a[0], a[1], f[0], f[1]);
}

/**
 * Each line's HTML as the browser serializes it back. highlight.js escapes quotes (&quot;,
 * &#x27;) but innerHTML returns them bare, so comparing against the raw string would call every
 * quoted line edited and rebuild it on each restore. Filled lazily; hand in a fresh array
 * whenever the lines change.
 */
export type SerializedLines = (string | undefined)[];

let scratch: HTMLTemplateElement | null = null;
function serialized(cache: SerializedLines, lineHtmls: readonly string[], i: number): string {
  let v = cache[i];
  if (v === undefined) {
    scratch ??= document.createElement("template");
    scratch.innerHTML = lineHtmls[i] ?? "";
    v = cache[i] = scratch.innerHTML;
  }
  return v;
}

/**
 * Puts every row's code cell back to the HTML React rendered, touching only the cells that
 * differ. Returns false when the edit broke the row structure itself (a node outside the
 * cells), which only a remount can undo.
 */
export function restoreGridRows(
  grid: HTMLElement,
  lineHtmls: readonly string[],
  cache: SerializedLines,
  rowCount: number,
  cellsPerRow: number,
): boolean {
  const rows = grid.childNodes;
  if (rows.length !== rowCount) return false;
  for (let i = 0; i < rows.length; i++) {
    const row = rows[i];
    if (!(row instanceof HTMLElement) || !row.classList.contains("cl-row") || row.childNodes.length !== cellsPerRow) return false;
    const code = row.lastChild;
    if (!(code instanceof HTMLElement) || !code.classList.contains("cl-code")) return false;
    const want = serialized(cache, lineHtmls, i);
    if (code.innerHTML !== want) code.innerHTML = want;
  }
  return true;
}
