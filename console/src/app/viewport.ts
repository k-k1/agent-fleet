// Keep the persistent frame (top bar / WS bar / chat header / mirror composer) pinned
// when the mobile soft keyboard opens.
//
// Android/GBoard is *meant* to be handled purely by the viewport meta's
// `interactive-widget=resizes-content` (index.html): the LAYOUT viewport itself
// shrinks, so `.app-shell`'s flex frame recomputes and only the chat body scrolls.
// In practice some Chrome/WebView versions ignore or only partially honor that hint,
// which leaves the frame full-height while the keyboard covers the bottom of it
// (e.g. the mirror composer) — the `--app-h` var below is also the fallback for that.
//
// iOS Safari ignores `interactive-widget` outright — it shrinks only the VISUAL
// viewport and pans the page to reveal the focused input, while the layout viewport
// (what 100% resolves against) stays full-height. That drags the pinned bars off the
// top. Here we mirror the visual viewport's height into a `--app-h` CSS var
// (consumed by `.app-shell` in app.css) so the frame fits the visible area instead,
// keeping the bars put and letting only the inner scroll containers scroll.
//
// The var is only set when a keyboard-sized shrink (>150px, same threshold as term.ts'
// keepInputVisible) is detected, so it's a strict no-op whenever the layout viewport
// already tracks the keyboard (desktop, and Android when resizes-content is honored,
// where innerHeight shrinks with the visual viewport → kb≈0).
export function wireViewport() {
  const vv = window.visualViewport;
  if (!vv) return;
  const root = document.documentElement;
  let kbOpen = false;
  const sync = () => {
    const kb = window.innerHeight - vv.height; // ~0 unless a soft keyboard is up
    kbOpen = kb > 150; // ignore URL-bar show/hide; only react to a keyboard
    if (kbOpen) root.style.setProperty("--app-h", `${vv.height}px`);
    else root.style.removeProperty("--app-h"); // fall back to height:100%
  };
  vv.addEventListener("resize", sync);
  vv.addEventListener("scroll", sync);
  // iOS still tries to scroll the focused input into view, panning the layout viewport
  // and dragging the pinned bars up with it. With the frame already fitted above the
  // keyboard the input is visible, so re-pin the page to the top. The app never scrolls
  // the window itself (every scroller is an inner overflow container), so this only ever
  // undoes the browser's focus auto-scroll.
  window.addEventListener(
    "scroll",
    () => {
      if (kbOpen && window.scrollY) window.scrollTo(0, 0);
    },
    { passive: true },
  );
  sync();
}
