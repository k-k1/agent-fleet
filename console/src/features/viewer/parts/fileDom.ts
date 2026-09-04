// The three helpers with which FileView touches the DOM directly. All of them live outside
// React and are pure functions (plus one side effect) that touch neither render nor state.
//
// Selection-to-line-number mapping reads the data-ln each code cell carries. It looks at
// logical lines rather than DOM text lines, so wrapping and highlighting cannot affect it.

// lineRangeOfSelection derives the 1-based line range + text of the current selection
// within the code grid. Each code cell carries data-ln (its 1-based logical line), so
// the selection's endpoints map to line numbers by walking up to their cell — wrap- and
// highlight-agnostic (it reads data-ln, not DOM text lines). Returns null if the
// selection is empty or not inside root.
export function lineRangeOfSelection(root: Element): { quote: string; startLine: number; endLine: number } | null {
  const sel = window.getSelection();
  if (!sel || sel.rangeCount === 0 || sel.isCollapsed) return null;
  const range = sel.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const quote = range.toString();
  if (!quote.trim()) return null;
  let a = lineNoOf(range.startContainer, root);
  let b = lineNoOf(range.endContainer, root);
  a = a ?? b;
  b = b ?? a;
  if (a == null || b == null) return null;
  return { quote, startLine: Math.min(a, b), endLine: Math.max(a, b) };
}

// Walk up from a selection endpoint to the nearest code cell and read its 1-based line
// number (data-ln). Returns null if the node isn't inside a code cell.
function lineNoOf(node: Node, root: Element): number | null {
  let el: HTMLElement | null = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
  while (el && el !== root) {
    if (el.dataset && el.dataset.ln) return parseInt(el.dataset.ln, 10);
    el = el.parentElement;
  }
  return null;
}

// A read-only file can use contentEditable for caret browsing, but that makes
// Android treat it as a text field and summon Gboard.  Reading on a touch device
// should leave the screen for the file, not the keyboard.
export function dismissSoftKeyboard(): void {
  const focused = document.activeElement;
  if (focused instanceof HTMLElement) focused.blur();
  const virtualKeyboard = (navigator as Navigator & { virtualKeyboard?: { hide?(): void } }).virtualKeyboard;
  virtualKeyboard?.hide?.();
}

export function escapeHtml(s: string) {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}
