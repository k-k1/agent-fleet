// Paints the PDF find bar's matches through the CSS Custom Highlight API (pdf.css styles them).
//
// The registry is global per document while each PDF pane owns its own matches, so every pane
// registers its ranges here and the named highlights are rebuilt from all of them; one pane
// setting CSS.highlights directly would wipe out another's. Without the API (jsdom, an old
// browser) find still moves between matches, only unpainted.

export const FIND_HIGHLIGHT = "pdf-find";
export const FIND_CURRENT = "pdf-find-current";

const owners = new Map<object, { all: Range[]; current: Range | null }>();

function supported(): boolean {
  return typeof CSS !== "undefined" && "highlights" in CSS && typeof Highlight !== "undefined";
}

/** Replaces one pane's highlighted matches; empty `all` and null `current` withdraw them. */
export function setFindHighlights(owner: object, all: Range[], current: Range | null): void {
  if (!supported()) return;
  if (all.length === 0 && !current) owners.delete(owner);
  else owners.set(owner, { all, current });
  const hits = new Highlight();
  const now = new Highlight();
  // The current match is painted over the rest, not instead of it.
  now.priority = 1;
  for (const o of owners.values()) {
    for (const r of o.all) hits.add(r);
    if (o.current) now.add(o.current);
  }
  CSS.highlights.set(FIND_HIGHLIGHT, hits);
  CSS.highlights.set(FIND_CURRENT, now);
}
