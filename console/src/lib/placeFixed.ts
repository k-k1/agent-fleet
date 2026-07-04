// placeFixed positions a position:fixed popover at the desired (left, top) but keeps
// it fully on-screen: it slides the menu left/up when it would run past the right/
// bottom edge, and never lets it go past the left/top edge. Used by the ＋ dropdown and
// the right-click context menus (Files, Repos) so none spill off a phone screen.
export function placeFixed(el: HTMLElement, left: number, top: number) {
  const pad = 8;
  const w = el.offsetWidth;
  const h = el.offsetHeight;
  const l = Math.max(pad, Math.min(left, window.innerWidth - w - pad));
  const t = Math.max(pad, Math.min(top, window.innerHeight - h - pad));
  el.style.left = l + "px";
  el.style.top = t + "px";
}
