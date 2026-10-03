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
// How long the swallow flag outlives a lift whose click the browser was told to drop. Touch
// browsers can hold a click back ~300 ms; past that the flag would only eat an unrelated click
// (a screen reader's double tap sends a click and no touches).
const SWALLOW_GRACE_MS = 800;

export function wireContextMenu(
  el: HTMLElement,
  open: (x: number, y: number) => void,
): { clickSwallowed: () => boolean } {
  let timer: number | null = null;
  let origin: { x: number; y: number } | null = null;
  let swallow = false;
  let swallowTimer: number | null = null;
  // Whether this gesture's pointerdown reached the link. The tap that puts an open menu away has
  // its pointerdown and touchend eaten by useDismiss in the window capture phase, so the
  // touchstart here still runs while the touchend that would cancel the timer never arrives:
  // without this gate the menu reopened 500 ms after being dismissed. Checked when the timer
  // fires, so the order of pointerdown and touchstart does not matter.
  let pointerSeen = false;

  const setSwallow = (on: boolean) => {
    swallow = on;
    if (swallowTimer !== null) {
      window.clearTimeout(swallowTimer);
      swallowTimer = null;
    }
  };

  // A touch-opened menu returns focus to whatever had it when it closes; on a phone that is often
  // the composer, and focusing it again brings the soft keyboard back up. Hand it the link.
  const openFromTouch = (x: number, y: number) => {
    el.focus({ preventScroll: true });
    open(x, y);
  };

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
    cancelTimer();
    if ((e as PointerEvent).pointerType === "touch") {
      setSwallow(true);
      openFromTouch(e.clientX, e.clientY);
    } else {
      open(e.clientX, e.clientY);
    }
  });
  el.addEventListener("pointerdown", (e) => {
    pointerSeen = e.pointerType !== "mouse";
  });
  // No click follows a right click, so a stale flag must not survive into the next mouse use.
  el.addEventListener("mousedown", () => {
    setSwallow(false);
  });
  el.addEventListener(
    "touchstart",
    (e) => {
      setSwallow(false);
      cancelTimer();
      const t = e.touches[0];
      if (!t || e.touches.length > 1) return;
      const { clientX, clientY } = t;
      origin = { x: clientX, y: clientY };
      timer = window.setTimeout(() => {
        timer = null;
        if (!pointerSeen) return; // the press that dismissed a menu, see pointerSeen
        pointerSeen = false;
        setSwallow(true);
        openFromTouch(clientX, clientY);
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
    pointerSeen = false;
    cancelTimer();
    if (!swallow) return;
    if (e.cancelable) e.preventDefault();
    swallowTimer = window.setTimeout(() => {
      swallowTimer = null;
      swallow = false;
    }, SWALLOW_GRACE_MS);
  });
  el.addEventListener("touchcancel", () => {
    pointerSeen = false;
    cancelTimer();
  });
  el.addEventListener("keydown", (e) => {
    if (!isContextMenuKey(e)) return;
    e.preventDefault();
    const a = menuAnchor(el);
    open(a.x, a.y);
  });

  return {
    clickSwallowed: () => {
      const s = swallow;
      setSwallow(false);
      return s;
    },
  };
}
