// Text search over a PDF's pages for the pane's own find bar (PdfView).
//
// The browser's find only sees text in the DOM and loses its place as the pane builds text
// layers under it, and it cannot be driven from a test, so the pane searches pdf.js's text
// content itself. Only pure functions live here; PdfView turns a match into a DOM Range over the
// text layer's spans, which hold the same item strings in the same order.

/** One page's searchable text: whitespace removed and characters folded, with the text-layer
 *  position (item index, UTF-16 offset in that item) of every remaining character. */
export interface PageText {
  text: string;
  item: Int32Array;
  offset: Int32Array;
}

/** A match on one page; the end is exclusive and always in the same coordinates as the start. */
export interface FindMatch {
  page: number;
  startItem: number;
  startOffset: number;
  endItem: number;
  endOffset: number;
}

const WHITESPACE = /\s/;
const KANGXI_RADICAL = /[\u2f00-\u2fd5]/;

/** Folds one UTF-16 unit so case and the producer's choice of look-alikes do not decide a match.
 *  It must return exactly one unit: positions are counted per unit. Kangxi radicals are what
 *  chromium's print-to-PDF writes for ideographs (see pdfText.ts). */
function fold(c: string): string {
  if (KANGXI_RADICAL.test(c)) {
    const n = c.normalize("NFKC");
    return n.length === 1 ? n : c;
  }
  const lower = c.toLowerCase();
  return lower.length === 1 ? lower : c;
}

/** Builds a page's searchable text from its text-content item strings.
 *
 *  Whitespace is dropped rather than kept, because a line break is not text in a PDF: an item
 *  ends at the end of a line and the next starts the next one, so a Japanese word wrapped across
 *  two lines would otherwise never match, and an English one would need the exact spacing. */
export function pageText(items: readonly string[]): PageText {
  let text = "";
  const item: number[] = [];
  const offset: number[] = [];
  items.forEach((s, k) => {
    for (let j = 0; j < s.length; j++) {
      const c = s[j];
      if (WHITESPACE.test(c)) continue;
      text += fold(c);
      item.push(k);
      offset.push(j);
    }
  });
  return { text, item: Int32Array.from(item), offset: Int32Array.from(offset) };
}

/** The query in the form pageText produces; empty when there is nothing to search for. */
export function foldQuery(query: string): string {
  let out = "";
  for (let j = 0; j < query.length; j++) if (!WHITESPACE.test(query[j])) out += fold(query[j]);
  return out;
}

/** Every match of the query, in reading order (page, then position). Matches do not overlap. */
export function findMatches(pages: readonly PageText[], query: string): FindMatch[] {
  const q = foldQuery(query);
  const out: FindMatch[] = [];
  if (!q) return out;
  pages.forEach((p, page) => {
    for (let at = p.text.indexOf(q); at >= 0; at = p.text.indexOf(q, at + q.length)) {
      const last = at + q.length - 1;
      out.push({
        page,
        startItem: p.item[at],
        startOffset: p.offset[at],
        endItem: p.item[last],
        endOffset: p.offset[last] + 1,
      });
    }
  });
  return out;
}

/** Index of the match to show first: the first one on or after the page being read, wrapping to
 *  the first match overall. -1 when there are none. */
export function firstMatchFrom(matches: readonly FindMatch[], page: number): number {
  if (matches.length === 0) return -1;
  const i = matches.findIndex((m) => m.page >= page);
  return i >= 0 ? i : 0;
}

/** Steps through the matches, wrapping at either end. */
export function stepMatch(index: number, count: number, dir: 1 | -1): number {
  if (count <= 0) return -1;
  if (index < 0) return dir > 0 ? 0 : count - 1;
  return (index + dir + count) % count;
}
