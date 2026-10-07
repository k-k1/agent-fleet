// Touch long-press that opens a context menu, for the surfaces whose rows have no native one on
// iOS Safari (it never fires `contextmenu` on a long press of a plain element). The same
// protocol as the reply-suggestion chips (SuggestChipMenu), without the menu itself:
//
//   - `props(open)` goes on the element: it starts createLongPress's timer on touchstart and
//     cancels it on touchmove (the finger is scrolling), touchend and touchcancel.
//   - `onContextMenu(e)` is called from the element's own native contextmenu handler. Android
//     delivers one for the same press, in either order with the timer; a touch-derived one marks
//     the lift's click as to be swallowed, a mouse right click must not (no click follows it, so
//     the flag would eat the next left click).
//   - `clickSwallowed()` is read first in the element's onClick: the lift of a fired long press
//     must not also open the folder it was pressed on. The flag is consumed by the call.
import { useEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, PointerEvent as RPointerEvent, TouchEvent as RTouchEvent } from "react";
import { createLongPress } from "./longPress.ts";

// How long after the finger lifts a click still counts as the long press's own.
const SWALLOW_MS = 600;

export type LongPressProps = {
  onPointerDown: (e: RPointerEvent) => void;
  onTouchStart: (e: RTouchEvent) => void;
  onTouchMove: (e: RTouchEvent) => void;
  onTouchEnd: (e: RTouchEvent) => void;
  onTouchCancel: () => void;
};

export type LongPressMenu = {
  props: (open: (x: number, y: number) => void) => LongPressProps;
  onContextMenu: (e: RMouseEvent) => void;
  clickSwallowed: () => boolean;
};

export function useLongPressMenu(): LongPressMenu {
  const [press] = useState(createLongPress);
  const swallow = useRef(false);
  const swallowTimer = useRef<number | null>(null);
  const dropSwallowTimer = () => {
    if (swallowTimer.current !== null) window.clearTimeout(swallowTimer.current);
    swallowTimer.current = null;
  };
  // A card or row that goes away mid-press (the listing refreshed, the pane moved on) must not
  // open a menu for something that is no longer on screen when its timer fires.
  useEffect(
    () => () => {
      press.end();
      dropSwallowTimer();
    },
    [press],
  );
  return {
    props: (open) => ({
      // The press that dismisses an open menu never reaches here (useDismiss eats it), which is
      // what stops that tap from reopening the menu 500 ms later; see createLongPress.
      onPointerDown: (e) => press.pointerDown(e.pointerType),
      onTouchStart: (e) => {
        swallow.current = false;
        const t = e.touches[0];
        if (!t || e.touches.length > 1) {
          press.cancel();
          return;
        }
        const { clientX, clientY } = t;
        // One hook can serve many rows (the Files tree), so the element that was pressed — not
        // the hook — is what has to still be there.
        const pressed = e.currentTarget as Element;
        press.start(clientX, clientY, () => {
          if (!pressed.isConnected) return;
          swallow.current = true;
          open(clientX, clientY);
        });
      },
      onTouchMove: (e) => {
        const t = e.touches[0];
        if (t) press.move(t.clientX, t.clientY);
      },
      // preventDefault on a fired press's lift: no compatibility click / mousedown follows, and
      // a synthesised mousedown would count as an outside press and close the menu at once.
      onTouchEnd: (e) => {
        if (swallow.current) {
          if (e.cancelable) e.preventDefault();
          // The lift's click, when the browser sends one anyway, comes straight after. A flag
          // that outlived it would eat the next unrelated click.
          dropSwallowTimer();
          swallowTimer.current = window.setTimeout(() => {
            swallow.current = false;
            swallowTimer.current = null;
          }, SWALLOW_MS);
        }
        press.end();
      },
      onTouchCancel: press.end,
    }),
    onContextMenu: (e) => {
      press.cancel();
      if ((e.nativeEvent as PointerEvent).pointerType === "touch") swallow.current = true;
    },
    clickSwallowed: () => {
      const s = swallow.current;
      swallow.current = false;
      return s;
    },
  };
}
