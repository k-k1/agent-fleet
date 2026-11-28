// Where the toast stack goes on a phone. At the bottom it covers the mirror's composer and draft
// bar, and taps meant for the input land on the toast; at the very top it covers the top bar and
// the workspace row (measured at 390x844: y 0-59 and 59-101). So it sits just below the run of
// bars that starts at the top of the frame, over the pane's content, which scrolls.

// The bars that stack down from the top of the frame: app chrome, then the top pane's tab strip
// and header, then a view's own strip of controls under its header, which the view marks with
// data-toast-chrome. A second pane's header lower down is not part of the run and is left out below.
export const TOAST_CHROME_SELECTOR = ".topbar, .popout-titlebar, .wsbar, .pane-tabs, .view-head, [data-toast-chrome]";

// A floating control that is not one of the bars but that a toast must still stay below, wherever
// the run of bars ends: the schedule editor's "return to editing" button. The run stops at the
// first bar of a second pane, and with a pane shrunk under a soft keyboard such a control can sit
// past that bar, out of the run's reach; a floor is applied after the run, so it still counts.
export const TOAST_FLOOR_SELECTOR = "[data-toast-floor]";
const WATCHED_SELECTOR = `${TOAST_CHROME_SELECTOR}, ${TOAST_FLOOR_SELECTOR}`;

// How far apart two bars of one run can be. Measured at 390x844: wsbar → pane header 7px, and the
// image studio's tab strip → the header of the session embedded under it 36px (column padding).
// A second pane's header is half a screen further down.
const RUN_GAP = 48;

export interface Box {
  top: number;
  bottom: number;
  left: number;
  right: number;
  // Which pane the bar belongs to; absent for the app's own bars. The run stays inside the first
  // pane it enters: with a split shrunk to its 20% minimum under a keyboard, the lower pane's
  // header sits within RUN_GAP of the upper one's and would pull the stack onto that pane's
  // composer.
  pane?: unknown;
}

// chromeBottom returns the bottom of the run of boxes that starts with the topmost one. It starts
// from that box, not from 0: while a soft keyboard is up the frame is shifted down by --app-top
// (app/viewport.ts), and the run moves with it.
export function chromeBottom(boxes: Box[], viewportWidth: number): number {
  const seen = boxes
    .filter((b) => b.bottom > b.top && b.right > 0 && b.left < viewportWidth)
    .sort((a, b) => a.top - b.top);
  if (seen.length === 0) return 0;
  let y = seen[0].top;
  let pane: unknown;
  for (const b of seen) {
    if (b.top > y + RUN_GAP) break;
    if (b.pane != null) {
      if (pane == null) pane = b.pane;
      else if (b.pane !== pane) break;
    }
    y = Math.max(y, b.bottom);
  }
  return y;
}

// `ignore` leaves one element (and what it holds) out: a floating element that places itself under
// the bars must not count itself, or each measurement would push it down by its own height.
export function measureChromeBottom(doc: Document = document, ignore?: Element | null): number {
  const counts = (e: Element) => !ignore || !ignore.contains(e);
  const boxes = [...doc.querySelectorAll(TOAST_CHROME_SELECTOR)].filter(counts).map((e) => {
    const r = e.getBoundingClientRect();
    return { top: r.top, bottom: r.bottom, left: r.left, right: r.right, pane: e.closest(".pane") ?? undefined };
  });
  let bottom = chromeBottom(boxes, doc.documentElement.clientWidth || window.innerWidth);
  for (const e of doc.querySelectorAll(TOAST_FLOOR_SELECTOR)) {
    if (!counts(e)) continue;
    const r = e.getBoundingClientRect();
    if (r.bottom > r.top) bottom = Math.max(bottom, r.bottom);
  }
  return bottom;
}

// touchesChrome says whether a batch of DOM mutations can have moved the bars: a bar added,
// removed or restyled, or an ancestor of one restyled (a pane switched, resized or hidden). The
// rest is content — a terminal's DOM renderer rewrites its rows every frame and a streaming
// transcript grows by the token — and measuring for it would force a layout each frame
// (panes.css records a 20s profile spent that way). Selector matching only; no layout here.
export function touchesChrome(records: MutationRecord[]): boolean {
  const hasBar = (n: Node) => n instanceof Element && (n.matches(WATCHED_SELECTOR) || n.querySelector(WATCHED_SELECTOR) != null);
  for (const r of records) {
    if (r.type === "attributes") {
      if (hasBar(r.target)) return true;
      continue;
    }
    for (const n of r.addedNodes) if (hasBar(n)) return true;
    for (const n of r.removedNodes) if (hasBar(n)) return true;
  }
  return false;
}
