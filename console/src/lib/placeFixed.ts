// placeFixed positions a position:fixed popover at the desired (left, top) but keeps
// it fully on-screen: it slides the menu left/up when it would run past the right/
// bottom edge, and never lets it go past the left/top edge. Used by the ＋ dropdown and
// the right-click context menus (Files, Repos) so none spill off a phone screen.
//
// `bounds` narrows the right/bottom limits to a container instead of the viewport —
// pass the left rail's scroll container so a context menu opened at the cursor stops
// short of that container's vertical scrollbar instead of painting over it. We use the
// container's clientWidth/Height (which exclude the scrollbar) rather than its border
// rect, so the menu never covers the scrollbar.
export function placeFixed(el: HTMLElement, left: number, top: number, bounds?: HTMLElement | null) {
  const pad = 8;
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  let maxRight = window.innerWidth;
  let maxBottom = window.innerHeight;
  if (bounds) {
    const r = bounds.getBoundingClientRect();
    maxRight = Math.min(maxRight, r.left + bounds.clientWidth);
    maxBottom = Math.min(maxBottom, r.top + bounds.clientHeight);
  }
  const l = Math.max(pad, Math.min(left, maxRight - w - pad));
  const t = Math.max(pad, Math.min(top, maxBottom - h - pad));
  el.style.left = l + "px";
  el.style.top = t + "px";
}
