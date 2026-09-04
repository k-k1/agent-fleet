// The arithmetic behind which page the PDF pane draws, at what size, and when
// (docs/log/82 §82.5).
//
// Only pure functions that never touch the DOM live here. Rendering depends on the canvas
// and the scroll position, so this layer is the only testable form of it: the failures that
// follow from getting it wrong (a canvas at the previous zoom surviving a zoom change, every
// off-screen page rendering at once and eating memory) all reproduce as errors in this
// arithmetic.

/** Intrinsic size of one page (pdf.js's scale=1 viewport, in CSS px). */
export interface PageSize {
  w: number;
  h: number;
}

/** Layout of the pages stacked vertically. tops[i] is the top y within the scroll
 *  container. */
export interface PageLayout {
  tops: number[];
  sizes: PageSize[];
  total: number;
}

/** Gap between pages and the outer padding; must match .pdfview-doc in the CSS. */
export const PAGE_GAP = 12;

/** The zoom steps. 1 means fit-to-width. */
export const ZOOM_STEPS: number[] = [0.5, 0.75, 1, 1.25, 1.5, 2, 3, 4];

/** Move one step from the current zoom. At either end it stays put. */
export function stepZoom(zoom: number, dir: 1 | -1): number {
  const steps = ZOOM_STEPS;
  if (dir > 0) return steps.find((s) => s > zoom + 1e-6) ?? steps[steps.length - 1];
  let out = steps[0];
  for (const s of steps) if (s < zoom - 1e-6) out = s;
  return out;
}

/** Zoom that fits the widest page to the container width.
 *
 *  Fitting to the widest page rather than each page keeps the column from jumping mid-read
 *  in a document that contains a single landscape page. When no width is available yet (not
 *  measured), it returns 1 so rendering waits for the next layout. */
export function fitScale(sizes: PageSize[], containerWidth: number, padding: number): number {
  const usable = containerWidth - padding * 2;
  if (!(usable > 0)) return 1;
  const widest = sizes.reduce((m, s) => Math.max(m, s.w), 0);
  if (!(widest > 0)) return 1;
  return usable / widest;
}

/** Position of each page and the total height at a given zoom. */
export function layoutPages(sizes: PageSize[], scale: number, gap = PAGE_GAP): PageLayout {
  const tops: number[] = [];
  const scaled: PageSize[] = [];
  let y = 0;
  for (const s of sizes) {
    const w = Math.max(1, Math.round(s.w * scale));
    const h = Math.max(1, Math.round(s.h * scale));
    tops.push(y);
    scaled.push({ w, h });
    y += h + gap;
  }
  return { tops, sizes: scaled, total: Math.max(0, y - gap) };
}

/** Range of page indices (0-based) to render now. overscan is how many viewports ahead to
 *  prefetch.
 *
 *  Returns the half-open interval [start, end). With no pages, start === end === 0. */
export function visibleRange(
  layout: PageLayout,
  scrollTop: number,
  viewportHeight: number,
  overscan = 0.5,
): { start: number; end: number } {
  const n = layout.tops.length;
  if (n === 0 || viewportHeight <= 0) return { start: 0, end: 0 };
  const margin = viewportHeight * overscan;
  const top = scrollTop - margin;
  const bottom = scrollTop + viewportHeight + margin;
  let start = n;
  let end = 0;
  for (let i = 0; i < n; i++) {
    const a = layout.tops[i];
    const b = a + layout.sizes[i].h;
    if (b < top || a > bottom) continue;
    if (i < start) start = i;
    if (i + 1 > end) end = i + 1;
  }
  return start > end ? { start: 0, end: 0 } : { start, end };
}

/** The page currently being read, for the info bar (1-based).
 *
 *  It is the page at the middle of the viewport. Deciding by the top edge would jump the
 *  number to the next, barely visible page the moment a page boundary reaches the top. */
export function currentPage(layout: PageLayout, scrollTop: number, viewportHeight: number): number {
  const n = layout.tops.length;
  if (n === 0) return 1;
  const probe = scrollTop + viewportHeight / 2;
  for (let i = n - 1; i >= 0; i--) if (layout.tops[i] <= probe) return i + 1;
  return 1;
}

/** Reading position remembered before a zoom change: page number plus the fraction within
 *  that page. */
export interface ScrollAnchor {
  page: number;
  fraction: number;
}

/** Extracts the current reading position in a zoom-independent form (page + fraction). */
export function anchorOf(layout: PageLayout, scrollTop: number): ScrollAnchor {
  const n = layout.tops.length;
  if (n === 0) return { page: 0, fraction: 0 };
  let page = 0;
  for (let i = n - 1; i >= 0; i--) {
    if (layout.tops[i] <= scrollTop) {
      page = i;
      break;
    }
  }
  const h = layout.sizes[page].h + PAGE_GAP;
  return { page, fraction: h > 0 ? (scrollTop - layout.tops[page]) / h : 0 };
}

/** Maps a remembered reading position back to a scroll position in the new zoom's
 *  layout. */
export function scrollTopForAnchor(layout: PageLayout, anchor: ScrollAnchor): number {
  const n = layout.tops.length;
  if (n === 0) return 0;
  const page = Math.min(Math.max(0, anchor.page), n - 1);
  const h = layout.sizes[page].h + PAGE_GAP;
  return Math.max(0, Math.round(layout.tops[page] + anchor.fraction * h));
}

/** Actual pixel count of a canvas: follows the display density but is capped by total pixels
 *  per page.
 *
 *  Holding many 4x canvases on a Retina display takes the whole tab down on a long document.
 *  The cap is set at roughly "an A4 page at 300dpi", and anything above it trades resolution
 *  to keep the area. */
export const MAX_CANVAS_PIXELS = 8_000_000;

export function canvasPixelRatio(cssW: number, cssH: number, dpr: number): number {
  const ratio = Math.min(Math.max(1, dpr), 3);
  const area = cssW * cssH * ratio * ratio;
  if (area <= MAX_CANVAS_PIXELS || cssW <= 0 || cssH <= 0) return ratio;
  return Math.max(0.5, Math.sqrt(MAX_CANVAS_PIXELS / (cssW * cssH)));
}
