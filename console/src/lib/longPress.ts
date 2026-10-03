// The touch long-press timer behind the context menus that have no native one on iOS Safari:
// the reply-suggestion chips (useChipMenu) and the links mdRefLinks.ts adds (wireContextMenu).
// The caller feeds it the gesture's events; it owns the timer, the move tolerance and the gate
// below, and leaves what a fired press does (open the menu, swallow the lift's click) to the caller.

// 500ms matches the browser's own long press (selection / callout).
export const LONG_PRESS_MS = 500;
// Further than this and the finger is scrolling (the transcript, the chip row), not pressing.
const MOVE_TOL = 10;

export type LongPress = {
  pointerDown: (pointerType: string) => void;
  /** Starts the timer for a touchstart at (x, y); fire runs once it holds for LONG_PRESS_MS. */
  start: (x: number, y: number, fire: () => void) => void;
  move: (x: number, y: number) => void;
  /** Cancels a pending press without ending the gesture (the menu opened by another route). */
  cancel: () => void;
  /** touchend / touchcancel. */
  end: () => void;
};

export function createLongPress(): LongPress {
  let timer: number | null = null;
  let origin: { x: number; y: number } | null = null;
  // Whether this gesture's pointerdown reached the element. The tap that puts an open menu away
  // has its pointerdown and touchend eaten by useDismiss in the window capture phase, so the
  // touchstart still arrives while the touchend that would cancel the timer never does: without
  // this gate the menu reopened 500 ms after being dismissed. Checked when the timer fires, so
  // the order of pointerdown and touchstart does not matter.
  let pointerSeen = false;

  const cancel = () => {
    if (timer !== null) {
      window.clearTimeout(timer);
      timer = null;
    }
    origin = null;
  };

  return {
    pointerDown: (pointerType) => {
      pointerSeen = pointerType !== "mouse";
    },
    start: (x, y, fire) => {
      cancel();
      origin = { x, y };
      timer = window.setTimeout(() => {
        timer = null;
        origin = null;
        if (!pointerSeen) return;
        pointerSeen = false;
        fire();
      }, LONG_PRESS_MS);
    },
    move: (x, y) => {
      if (origin && (Math.abs(x - origin.x) > MOVE_TOL || Math.abs(y - origin.y) > MOVE_TOL)) cancel();
    },
    cancel,
    end: () => {
      pointerSeen = false;
      cancel();
    },
  };
}
