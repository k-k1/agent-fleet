// wireContextMenu gives a plain DOM element (one built outside React, like the links
// mdRefLinks.ts adds after a Markdown render) a context menu reachable from every input:
//   - contextmenu event ... a mouse right click, and Android's own long press
//   - long press (500ms) ... touch. iOS Safari never fires contextmenu, so without the timer
//     a phone has no way in at all.
//   - Menu key / Shift+F10 ... keyboard, anchored at the element (contextMenuKey.ts).
// The rules are the ones useChipMenu (SuggestChipMenu.tsx) settled for the suggestion chips.
//
// After a touch opened the menu, the click the lift produces would also run the element's
// primary action (open the session behind the menu), so it is swallowed exactly once — the
// element's own click listener asks through the returned clickSwallowed().
import { isContextMenuKey, menuAnchor } from "../../project/contextMenuKey.ts";

// 500ms matches the browser's own long press (selection / callout).
export const LONG_PRESS_MS = 500;
// Further than this and the finger is scrolling the transcript, not pressing.
const MOVE_TOL = 10;

export function wireContextMenu(
  el: HTMLElement,
  open: (x: number, y: number) => void,
): { clickSwallowed: () => boolean } {
  let timer: number | null = null;
  let origin: { x: number; y: number } | null = null;
  let swallow = false;

  const cancelTimer = () => {
    if (timer !== null) {
      window.clearTimeout(timer);
      timer = null;
    }
    origin = null;
  };

  el.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    // Only a touch-derived contextmenu is followed by a click; setting the flag on a mouse
    // right click would eat the next left click.
    if ((e as PointerEvent).pointerType === "touch") swallow = true;
    cancelTimer();
    open(e.clientX, e.clientY);
  });
  // No click follows a right click, so a stale flag must not survive into the next mouse use.
  el.addEventListener("mousedown", () => {
    swallow = false;
  });
  el.addEventListener(
    "touchstart",
    (e) => {
      swallow = false;
      cancelTimer();
      const t = e.touches[0];
      if (!t || e.touches.length > 1) return;
      const { clientX, clientY } = t;
      origin = { x: clientX, y: clientY };
      timer = window.setTimeout(() => {
        timer = null;
        swallow = true;
        open(clientX, clientY);
      }, LONG_PRESS_MS);
    },
    // Passive: the transcript must keep scrolling when a swipe starts on a link.
    { passive: true },
  );
  el.addEventListener(
    "touchmove",
    (e) => {
      const t = e.touches[0];
      if (!t || !origin) return;
      if (Math.abs(t.clientX - origin.x) > MOVE_TOL || Math.abs(t.clientY - origin.y) > MOVE_TOL) cancelTimer();
    },
    { passive: true },
  );
  // Once the long press has fired, cancel the touchend so no compatibility mousedown / click is
  // synthesised: that mousedown is an outside press to useDismiss and would close the menu as
  // the finger lifts. The flag stays as the fallback for a browser that ignores this.
  el.addEventListener("touchend", (e) => {
    if (swallow && e.cancelable) e.preventDefault();
    cancelTimer();
  });
  el.addEventListener("touchcancel", cancelTimer);
  el.addEventListener("keydown", (e) => {
    if (!isContextMenuKey(e)) return;
    e.preventDefault();
    const a = menuAnchor(el);
    open(a.x, a.y);
  });

  return {
    clickSwallowed: () => {
      const s = swallow;
      swallow = false;
      return s;
    },
  };
}
