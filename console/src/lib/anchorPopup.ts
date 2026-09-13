import { zoom } from "../app/viewport.ts";

// anchorPopup places a position:fixed popup against the field that opened it, and keeps it
// inside the area the reader can actually SEE. It is the model picker's placement; the
// general menus use placeFixed, which only clamps to the layout viewport and cannot flip or
// resize (a menu of five rows never needs to).
//
// Two things a dropdown needs that a context menu does not:
//
//   - **Flip above when there is no room below.** A field near the bottom of a dialog would
//     otherwise get a list squeezed into the last few pixels, or slid up until it covers the
//     field it belongs to.
//   - **Fit the height to the space left.** The list caps itself in CSS; when less room than
//     that is available it has to shrink and scroll rather than run off the edge.
//
// And the reason both are measured against the VISUAL viewport: a soft keyboard shrinks the
// visual viewport, but on iOS the layout viewport — which is what position:fixed coordinates
// and window.innerHeight are in — does not move at all. Placing against innerHeight puts the
// list behind the keyboard. The >150px test and the pinch-zoom exclusion are the same ones
// app/viewport.ts uses, and for the same reason: at 2x zoom the visual viewport is half the
// layout viewport with no keyboard anywhere.
const PAD = 8; // keep off the very edge, as placeFixed does
const GAP = 2; // between the field and the list
const MIN_H = 96; // below this the list is useless; flip or overflow the pad instead

/** The visible band in LAYOUT pixels — the coordinate system position:fixed is placed in. */
function visibleBand(): { top: number; bottom: number } {
  const vv = typeof window !== "undefined" ? window.visualViewport : null;
  if (vv && zoom(vv) <= 1.01 && window.innerHeight - vv.height > 150) {
    return { top: vv.offsetTop, bottom: vv.offsetTop + vv.height };
  }
  return { top: 0, bottom: window.innerHeight };
}

/**
 * Position `el` under (or over) `anchor`. Call it whenever either could have moved — a
 * scroll of any ancestor, a resize, the keyboard opening — not just when the popup opens.
 */
export function anchorPopup(el: HTMLElement, anchor: HTMLElement): void {
  const a = anchor.getBoundingClientRect();
  const band = visibleBand();
  const below = band.bottom - PAD - (a.bottom + GAP);
  const above = a.top - GAP - (band.top + PAD);

  // Measure at the CSS cap first, so the cap stays written in one place (the stylesheet) and
  // the inline value only ever shrinks it.
  el.style.maxHeight = "";
  el.style.width = a.width + "px";
  const wanted = el.offsetHeight;
  // Flip only when below genuinely cannot hold a usable list and above can do better —
  // otherwise the list jumps sides as the dialog scrolls by a few pixels.
  const flip = wanted > below && below < MIN_H && above > below;
  const space = flip ? above : below;
  if (wanted > space) el.style.maxHeight = Math.max(MIN_H, space) + "px";

  const h = el.offsetHeight;
  const top = flip ? a.top - GAP - h : a.bottom + GAP;
  const left = Math.max(PAD, Math.min(a.left, window.innerWidth - el.offsetWidth - PAD));
  el.style.left = left + "px";
  el.style.top = Math.max(band.top + PAD, Math.min(top, band.bottom - PAD - h)) + "px";
}
