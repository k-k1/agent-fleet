// Paints the PDF find bar's matches through the CSS Custom Highlight API (pdf.css styles them).
//
// The registry is global per document while each PDF pane owns its own matches, so the two named
// highlights are shared and every pane adds and removes only its own ranges; one pane setting
// CSS.highlights directly would wipe out another's. Updates are incremental because text layers
// arrive one page at a time: rebuilding every range per page is pages x matches work on a long
// document with a common query. Without the API (jsdom, an old browser) find still moves between
// matches, only unpainted.

export const FIND_HIGHLIGHT = "pdf-find";
export const FIND_CURRENT = "pdf-find-current";

const hitsOf = new Map<object, Range[]>();
const currentOf = new Map<object, Range>();
let hits: Highlight | null = null;
let current: Highlight | null = null;

function registry(): { hits: Highlight; current: Highlight } | null {
  if (typeof CSS === "undefined" || !("highlights" in CSS) || typeof Highlight === "undefined") return null;
  if (!hits || !current || CSS.highlights.get(FIND_HIGHLIGHT) !== hits) {
    hits = new Highlight();
    current = new Highlight();
    // The current match is painted over the rest, not instead of it.
    current.priority = 1;
    for (const rs of hitsOf.values()) for (const r of rs) hits.add(r);
    for (const r of currentOf.values()) current.add(r);
    CSS.highlights.set(FIND_HIGHLIGHT, hits);
    CSS.highlights.set(FIND_CURRENT, current);
  }
  return { hits, current };
}

/** Adds ranges to one pane's painted matches. */
export function addFindHits(owner: object, ranges: Range[]): void {
  const reg = registry();
  if (!reg || ranges.length === 0) return;
  const own = hitsOf.get(owner) ?? [];
  for (const r of ranges) {
    own.push(r);
    reg.hits.add(r);
  }
  hitsOf.set(owner, own);
}

/** Withdraws everything one pane painted, the current match included. */
export function clearFindHits(owner: object): void {
  const reg = registry();
  if (!reg) return;
  for (const r of hitsOf.get(owner) ?? []) reg.hits.delete(r);
  hitsOf.delete(owner);
  setFindCurrent(owner, null);
}

/** Sets (or with null, clears) one pane's current match. */
export function setFindCurrent(owner: object, range: Range | null): void {
  const reg = registry();
  if (!reg) return;
  const old = currentOf.get(owner);
  if (old === range) return;
  if (old) reg.current.delete(old);
  if (range) {
    currentOf.set(owner, range);
    reg.current.add(range);
  } else {
    currentOf.delete(owner);
  }
}
