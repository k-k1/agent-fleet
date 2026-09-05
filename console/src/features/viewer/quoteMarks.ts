// Tools for counting, restoring and highlighting quoted passages inside rendered Markdown.
//
// Why "quote string + occurrence number" rather than an offset: MarkdownView renders the
// Markdown through innerHTML, so the character positions of the original source are gone.
// The rendered text, by contrast, is stable, so the nth match within it is the anchor (the
// same idea as W3C Web Annotation's TextQuoteSelector). When the body is revised and no
// longer matches, the highlight simply does not appear; it never lands on the wrong passage.
//
// Counting must happen on the NORMALIZED text. `Selection.toString()`, the source of the
// selected string, is rendered text — CSS whitespace collapsing applies and the boundaries of
// `<br>`, paragraphs and list items gain newlines that the DOM never had — while a
// concatenation of textContent is raw text. Measured: a plain `indexOf` therefore missed on
// the very next selection and the picker silently stopped appearing:
//   - across a source newline inside a paragraph  selection "a b"      / raw "a\nb"
//   - across a `<br>`                             selection "one\ntwo" / raw "onetwo"
//                                                 (a br contributes nothing to textContent)
//   - across paragraphs or list items             selection "x.\n\ny"  / raw has no separator
// So both the capture side and the restore side count over text whose whitespace runs have
// been collapsed to a single space (`normalizeQuote` / `flattenRoot`). That absorbs only
// differences in the SHAPE of whitespace, so the "never lands on the wrong passage" property
// is preserved.
//
// The pure functions (occurrenceOf / indexOfNth / normalizeQuote) are kept apart from the DOM
// manipulation so counting regressions can be pinned by node-project tests.

/** How many times quote occurs in text[0, start) = the occurrence number of the match
 *  starting at start. */
export function occurrenceOf(text: string, quote: string, start: number): number {
  if (!quote) return 0;
  let n = 0;
  for (let i = text.indexOf(quote); i >= 0 && i < start; i = text.indexOf(quote, i + 1)) n++;
  return n;
}

/** Start position of the nth match (0-based), or -1 if there is none. */
export function indexOfNth(text: string, quote: string, nth: number): number {
  if (!quote) return -1;
  let i = text.indexOf(quote);
  for (let n = 0; i >= 0 && n < nth; n++) i = text.indexOf(quote, i + 1);
  return i;
}

/** Collapse whitespace runs to one space, erasing the whitespace-shape difference between
 *  rendered text and raw text. */
export function normalizeQuote(s: string): string {
  return s.replace(/\s+/g, " ").trim();
}

/** Elements that appear to have a line break around them. A selection crossing one gains a
 *  separator the raw text does not have. */
const BLOCK_TAGS = new Set([
  "ADDRESS", "ARTICLE", "ASIDE", "BLOCKQUOTE", "BR", "DD", "DETAILS", "DIV", "DL", "DT",
  "FIGCAPTION", "FIGURE", "FOOTER", "FORM", "H1", "H2", "H3", "H4", "H5", "H6", "HEADER",
  "HR", "LI", "MAIN", "NAV", "OL", "P", "PRE", "SECTION", "SUMMARY", "TABLE", "TD", "TH",
  "TR", "UL",
]);

interface TextSpan {
  node: Text;
  start: number; // start position within the root's raw text
}

interface FlatRoot {
  spans: TextSpan[];
  /** The text after whitespace collapsing. Always count over this one. */
  text: string;
  /** Which raw-text character text[i] came from (non-decreasing). Used to map back to raw
   *  positions when painting. */
  rawAt: number[];
}

// Collects the text nodes in document order, records each node's start offset, and builds
// the normalized text plus the normalized-to-raw mapping. Highlight <mark> elements are not
// excluded from the count: characters inside an existing mark are still part of the body.
function flattenRoot(root: HTMLElement): FlatRoot {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT | NodeFilter.SHOW_ELEMENT);
  const spans: TextSpan[] = [];
  const rawAt: number[] = [];
  let rawLen = 0;
  let text = "";
  let pending = false; // whitespace or an element boundary was just seen
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if (n.nodeType === Node.ELEMENT_NODE) {
      if (BLOCK_TAGS.has((n as Element).tagName)) pending = true;
      continue;
    }
    const node = n as Text;
    const value = node.nodeValue || "";
    spans.push({ node, start: rawLen });
    for (let i = 0; i < value.length; i++) {
      if (/\s/.test(value[i])) {
        pending = true;
        continue;
      }
      if (pending) {
        pending = false;
        // Drop leading whitespace: the body never starts with a space, so the count does
        // not shift by one character.
        if (text.length) {
          text += " ";
          rawAt.push(rawLen + i);
        }
      }
      text += value[i];
      rawAt.push(rawLen + i);
    }
    rawLen += value.length;
  }
  return { spans, text, rawAt };
}

/** Which character of the whole raw text (node, offset) is within root. At an element
 *  boundary, the head of the next text node. */
function offsetIn(spans: TextSpan[], node: Node, offset: number): number {
  for (const s of spans) {
    if (s.node === node) return s.start + offset;
  }
  // A double click, or a selection starting at the head of a paragraph, gives an element as
  // startContainer. Treat the first text node at or after that position as the start;
  // falling back to a search from the beginning would pick up another occurrence of the same
  // word.
  if (node.nodeType !== Node.ELEMENT_NODE) return -1;
  const probe = document.createRange();
  probe.setStart(node, Math.min(offset, node.childNodes.length));
  probe.collapse(true);
  for (const s of spans) {
    try {
      if (probe.comparePoint(s.node, 0) >= 0) return s.start;
    } catch {
      /* skip positions that cannot be compared */
    }
  }
  return -1;
}

/** Raw-text position to normalized-text position (the first character at or after it). */
function normIndexOf(rawAt: number[], raw: number): number {
  let lo = 0;
  let hi = rawAt.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (rawAt[mid] < raw) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

export interface QuoteAnchor {
  quote: string;
  nth: number;
}

/**
 * Builds a quote anchor from a range and the string the selection visually reads as.
 *
 * `selected` is a parameter because in jsdom `Selection.toString()` (rendered text) returns
 * raw text, which would make the very difference this function absorbs impossible to
 * reproduce in a test.
 */
export function anchorForRange(root: HTMLElement, range: Range, selected: string): QuoteAnchor | null {
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const quote = normalizeQuote(selected);
  if (!quote) return null;
  const { spans, text, rawAt } = flattenRoot(root);
  const rawStart = offsetIn(spans, range.startContainer, range.startOffset);
  // Leading whitespace in the selection shifts the position, so re-take the actual match
  // position from start onwards.
  const from = rawStart < 0 ? 0 : Math.max(0, normIndexOf(rawAt, rawStart) - quote.length);
  const at = text.indexOf(quote, from);
  if (at < 0) return null;
  return { quote, nth: occurrenceOf(text, quote, at) };
}

/** Turns the selection currently settled inside root into a quote anchor. null when there is
 *  no selection or it lies outside root. */
export function selectionAnchor(root: HTMLElement): (QuoteAnchor & { rect: DOMRect }) | null {
  const sel = window.getSelection();
  if (!sel || sel.isCollapsed || sel.rangeCount === 0) return null;
  const range = sel.getRangeAt(0);
  const anchor = anchorForRange(root, range, sel.toString());
  if (!anchor) return null;
  return { ...anchor, rect: range.getBoundingClientRect() };
}

/** Removes the applied highlights, re-joining the text nodes as they were. */
export function clearMarks(root: HTMLElement, selector: string): void {
  const marks = [...root.querySelectorAll<HTMLElement>(selector)];
  for (const m of marks) {
    const parent = m.parentNode;
    if (!parent) continue;
    while (m.firstChild) parent.insertBefore(m.firstChild, m);
    parent.removeChild(m);
  }
  if (marks.length) root.normalize(); // rejoin split text nodes so the next count stays right
}

/** Removes the highlights applyQuoteMarks applied. */
export function clearQuoteMarks(root: HTMLElement): void {
  clearMarks(root, "mark.quote-mark");
}

/** One mark to apply: where (quote/nth) and how it looks (className/dataset). */
export interface PaintedMark extends QuoteAnchor {
  className: string;
  dataset?: Record<string, string>;
}

/**
 * Wraps each anchored passage in a <mark>. The return value says which anchors were actually
 * found, so the card side can grey out comments whose passage a revision removed.
 *
 * One quote can span several elements (a selection across paragraphs, the middle of bold
 * text), so Range.surroundContents is not used; each overlapping text node is cut and wrapped
 * separately. Walking while mutating the DOM would corrupt the positions, so the targets are
 * collected first and rewritten afterwards (the same discipline as MarkdownView's
 * renderEmoji).
 *
 * `selector` is what removes marks this function applied last time. Each surface uses its own
 * class so plan-comment quotes and transcript marks do not erase each other.
 */
export function applyPaintedMarks(root: HTMLElement, marks: PaintedMark[], selector: string): boolean[] {
  clearMarks(root, selector);
  const { spans, text, rawAt } = flattenRoot(root);
  const found = marks.map(() => false);
  // Stored quotes are collapsed before counting too, so an older mark containing newlines
  // still resolves. Processing back to front keeps already-settled positions correct even
  // when the same text node is cut twice.
  const targets = marks
    .map((m, i) => {
      const quote = normalizeQuote(m.quote);
      const at = quote ? indexOfNth(text, quote, m.nth) : -1;
      // Map [at, at+len) in the normalized text back to the raw-text range used for cutting.
      return { i, at, from: at < 0 ? -1 : rawAt[at], to: at < 0 ? -1 : rawAt[at + quote.length - 1] + 1 };
    })
    .filter((x) => x.at >= 0)
    .sort((a, b) => b.from - a.from);

  for (const target of targets) {
    const end = target.to;
    for (const s of spans) {
      const node = s.node;
      const nodeEnd = s.start + (node.nodeValue || "").length;
      if (nodeEnd <= target.from || s.start >= end) continue; // no overlap
      if (!node.parentNode) continue; // replaced by an earlier cut
      // A quote spanning paragraphs or list items also covers the whitespace nodes between
      // blocks. Painting them changes nothing visually and would put a <mark> directly under
      // a <ul>, so skip them.
      if (!/\S/.test(node.nodeValue || "")) continue;
      const from = Math.max(0, target.from - s.start);
      const to = Math.min((node.nodeValue || "").length, end - s.start);
      const mark = document.createElement("mark");
      mark.className = marks[target.i].className;
      for (const [k, v] of Object.entries(marks[target.i].dataset || {})) mark.dataset[k] = v;
      const range = document.createRange();
      range.setStart(node, from);
      range.setEnd(node, to);
      try {
        range.surroundContents(mark); // always valid: the range is within one text node
        found[target.i] = true;
      } catch {
        /* unexpected structure: give up on that highlight rather than damage the body */
      }
    }
  }
  return found;
}

/** Quote highlights for plan comments, with a number badge. */
export function applyQuoteMarks(root: HTMLElement, anchors: QuoteAnchor[]): boolean[] {
  return applyPaintedMarks(
    root,
    anchors.map((a, i) => ({ ...a, className: "quote-mark", dataset: { n: String(i + 1) } })),
    "mark.quote-mark",
  );
}
