// May a horizontal swipe be taken over as a screen gesture? Entry guard for the phone's
// left-swipe (rotate through running sessions, App.tsx).
//
// The listener is a passive one on window, so touchmove arrives wherever the finger is.
// Reacting unconditionally would misread things like "I meant to scroll a code block
// sideways" as a session change, so we walk up from the element the gesture started on
// and stand down when it sits on a surface that already owns horizontal interaction:
//   - the browser pane (.browser-stage) — touches are forwarded to the Chromium inside
//   - inputs / contenteditable — caret and selection dragging
//   - surfaces that pan sideways (pre, tables, tab strips, suggestion chip rows) =
//     pansHorizontally
//   - explicit opt-out [data-no-swipe]
// Conversely, a scroll container meant to be read vertically can declare "horizontal
// overflow here is an accident" with [data-swipe-y]; that element is then not counted as
// a horizontal scroller (see the note on pansHorizontally). The decision is made from the
// start element only (once, at touchstart). A pure DOM function, so it is unit-testable
// under the dom vitest environment.

const EDITABLE_TAGS = new Set(["INPUT", "TEXTAREA", "SELECT"]);

/** Is this element itself an editing surface? We read the attribute rather than
 * isContentEditable because the ancestor walk already covers inheritance and jsdom does
 * not implement isContentEditable (a guard that only holds in a real browser is no
 * guard). */
function editable(el: Element): boolean {
  if (EDITABLE_TAGS.has(el.tagName)) return true;
  const ce = el.getAttribute("contenteditable");
  return ce !== null && ce !== "false";
}

/** Is this element itself a surface that pans sideways (content overflows and overflow-x
 * is scrollable)?
 *
 * The computed overflow-x alone would sweep in vertical scrollers: when one of
 * overflow-x/y is visible and the other is not, CSS computes the visible one to auto, so
 * an element that only sets `overflow-y: auto` still reads back "auto" for overflow-x
 * (measured in a real browser). That leaves the test effectively as scrollWidth >
 * clientWidth alone.
 *
 * That is what made swipe-to-switch dead on particular sessions: a single unbreakable
 * long string in the transcript (sha256:…, a URL with a query, a long identifier) pushes
 * the transcript's scroll container (.mirror-body) into horizontal overflow (measured at
 * 390px wide: sw=633/cw=390). That container is an ancestor of every point in the
 * transcript, so a swipe over an ordinary paragraph was rejected too, and because
 * scrollWidth is the whole transcript's, scrolling that line off screen or reopening the
 * session did not clear it.
 *
 * So a vertically-read surface can declare "horizontal overflow here is an accident" with
 * [data-swipe-y] and is then not counted as a horizontal scroller. The test itself is
 * unchanged, so surfaces that genuinely pan both ways (code view, diffs, clamped ASCII
 * mockups) are not waved through by a guess like "only things that don't scroll
 * vertically are horizontal scrollers". */
function pansHorizontally(el: Element): boolean {
  if (el.hasAttribute("data-swipe-y")) return false;
  if (el.scrollWidth <= el.clientWidth + 1) return false;
  const ox = el.ownerDocument.defaultView?.getComputedStyle(el).overflowX;
  return ox === "auto" || ox === "scroll" || ox === "overlay";
}

/** Given the element a gesture started on, must the horizontal swipe be left alone? */
export function swipeBlocked(target: EventTarget | null): boolean {
  let el: Element | null = target instanceof Element ? target : null;
  // The depth cap is insurance: an unexpectedly deep DOM must not stall touchstart.
  for (let depth = 0; el && depth < 60; depth++, el = el.parentElement) {
    if (editable(el)) return true;
    if (el.hasAttribute("data-no-swipe")) return true;
    if (el.classList.contains("browser-stage")) return true;
    if (pansHorizontally(el)) return true;
  }
  return false;
}
