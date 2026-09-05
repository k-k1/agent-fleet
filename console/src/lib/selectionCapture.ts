// selectionCapture — the one subscription that turns a settled text selection into a floating
// control, plus the hold that keeps a finger resting on that control from erasing it.
//
// Six surfaces (transcript marks, the two read-aloud pills, the viewer's send pill, plan
// comments) each carried a byte-identical copy of the debounced `selectionchange` effect. They
// also shared one touch-only defect: tapping the control collapses the selection, which fires
// `selectionchange` at once, and SELECT_DEBOUNCE later the capture runs, finds no selection and
// clears the very control the finger is still on — so a tap held longer than the debounce did
// nothing at all. A mouse is immune because every button cancels the mousedown that would
// collapse the selection; on touch the synthetic mousedown arrives long after the selection is
// already gone, so preventDefault there cannot help.
//
// Hence the hold: a press suspends capture until shortly after release, then runs it once so the
// control still matches reality. It does not try to keep the selection alive — each surface
// copies what it needs (quote, nth, line range, block index) into state at capture time, so the
// action works from a collapsed selection.

import { useEffect, useRef } from "react";

/** How long to wait for a selection to settle before reading it. */
export const SELECT_DEBOUNCE = 250;
/** How long a release keeps suppressing capture. Must exceed SELECT_DEBOUNCE, so that the timer
 *  armed by the press itself is swallowed rather than firing just after the finger lifts. */
const HOLD_GRACE = 400;

let pressed = false;
let heldUntil = 0;
const releaseListeners = new Set<() => void>();

/** Is a selection control being pressed (or just released)? Capture is suspended while true. */
export function selectionHeld(): boolean {
  return pressed || Date.now() < heldUntil;
}

/**
 * Called on pointerdown on a selection control (SelectionFloat wires this for its whole group).
 * Self-releasing: the pointerup may land anywhere — outside the control, or on nothing at all
 * when the gesture is cancelled — so the release listeners go on the window, in the capture
 * phase, and remove themselves.
 */
export function holdSelection(): void {
  if (pressed) return;
  pressed = true;
  heldUntil = 0;
  const release = () => {
    window.removeEventListener("pointerup", release, true);
    window.removeEventListener("pointercancel", release, true);
    pressed = false;
    heldUntil = Date.now() + HOLD_GRACE;
    // Re-read the selection once the grace is over: the action has run by then, and whatever it
    // left behind (usually a collapsed selection) is what the control should reflect.
    setTimeout(() => {
      if (pressed) return; // pressed again in the meantime — that press will run it instead
      for (const fn of [...releaseListeners]) fn();
    }, HOLD_GRACE + 20);
  };
  window.addEventListener("pointerup", release, true);
  window.addEventListener("pointercancel", release, true);
}

/**
 * Subscribes `capture` to selection changes: debounced (the selection updates continuously while
 * a drag handle moves), suspended while a control is held, and run once when a hold releases.
 *
 * A touch long-press selection emits no mouseup, so `selectionchange` — not mouseup — is what
 * makes these controls exist at all on a phone. Surfaces that also capture from mouseup keep
 * doing so; this only adds the path mouseup misses.
 */
export function useSelectionCapture(capture: () => void, debounceMs: number = SELECT_DEBOUNCE): void {
  // The effect subscribes once, so the latest closure is reached through a ref rather than by
  // resubscribing on every render.
  const ref = useRef(capture);
  ref.current = capture;
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const run = () => {
      // Checked when the timer fires, not when it is armed: the press is what arms it.
      if (selectionHeld()) return;
      ref.current();
    };
    const onSelChange = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(run, debounceMs);
    };
    const onRelease = () => ref.current();
    document.addEventListener("selectionchange", onSelChange);
    releaseListeners.add(onRelease);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      releaseListeners.delete(onRelease);
      if (timer) clearTimeout(timer);
    };
  }, [debounceMs]);
}
