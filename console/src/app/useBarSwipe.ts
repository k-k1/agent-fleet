// Vertical swipe on the activity bar: steps the left pane's view on a phone (ADR 0140).
//
// Own listeners on the bar rather than a branch in installSwipeGestures: that module is
// horizontal-only and window-level, and this gesture must start on the bar. The two share
// its constants and guards (long-press window, one finger, pinch-zoomed page) instead of
// re-deriving them.
//
// The listeners are passive. Pulling the page down (Chrome's pull-to-refresh, iOS
// rubber-banding) is kept off by CSS instead — `touch-action: pan-x` on `.actbar[data-fits]`,
// which this hook maintains — because preventDefault on a touch that already started
// scrolling is ignored, and a non-passive listener would block scrolling for everything
// under the bar.
import { useEffect, useRef } from "react";
import type { RefObject } from "react";
import { barFits, barSwipeDelta } from "./barSwipe.ts";
import { LONG_PRESS_MS } from "./swipeGestures.ts";
import { zoom } from "./viewport.ts";

export interface BarSwipeOptions {
  /** Is the gesture live at all (phone width, drawer open)? Read at touchstart. */
  enabled: boolean;
  /** Step the view by delta (+1 next, -1 previous). Returns false at an end of the list. */
  step(delta: 1 | -1): boolean;
}

/** After a committed swipe, a click arriving within this window is swallowed. The browser
 * fires it straight after touchend, so the window only has to outlast that; a timer is what
 * stops the suppression from leaking onto the next unrelated tap. */
const CLICK_SWALLOW_MS = 400;

export function useBarSwipe(ref: RefObject<HTMLElement | null>, opts: BarSwipeOptions, deps: unknown[] = []) {
  // Read through a ref so a changing view list re-targets the gesture without re-attaching
  // listeners mid-touch.
  const optsRef = useRef(opts);
  optsRef.current = opts;

  // data-fits drives the CSS touch-action; recompute whenever the layout can have changed.
  // scrollHeight does not raise a resize event of its own, hence also on every render
  // (deps) and on window resize.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const sync = () => {
      if (optsRef.current.enabled && barFits(el)) el.setAttribute("data-fits", "");
      else el.removeAttribute("data-fits");
    };
    sync();
    window.addEventListener("resize", sync);
    const ro = typeof ResizeObserver === "undefined" ? null : new ResizeObserver(sync);
    ro?.observe(el);
    return () => {
      window.removeEventListener("resize", sync);
      ro?.disconnect();
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ref, opts.enabled, ...deps]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    let sx = 0,
      sy = 0,
      armed = false,
      timer: number | null = null,
      swallowUntil = 0,
      // The element the finger went down on. Touch events keep targeting it, and if it is
      // removed mid-gesture (a conditional button) its touchend no longer bubbles to the bar.
      startTarget: EventTarget | null = null;

    const unwatchTarget = () => {
      startTarget?.removeEventListener("touchend", onEnd);
      startTarget?.removeEventListener("touchcancel", onCancel);
      startTarget = null;
    };

    const disarm = () => {
      armed = false;
      if (timer !== null) {
        window.clearTimeout(timer);
        timer = null;
      }
    };
    const onStart = (e: TouchEvent) => {
      disarm();
      unwatchTarget();
      // A new touch ends any earlier suppression: the next tap must always work.
      swallowUntil = 0;
      if (!optsRef.current.enabled || e.touches.length !== 1) return;
      const vv = window.visualViewport;
      if (vv && zoom(vv) > 1.01) return;
      // An overflowing column scrolls natively; decided once, here, from its current size.
      if (!barFits(el)) return;
      const touch = e.touches[0];
      if (!touch) return;
      sx = touch.clientX;
      sy = touch.clientY;
      armed = true;
      timer = window.setTimeout(disarm, LONG_PRESS_MS);
    };
    const onMove = (e: TouchEvent) => {
      if (!armed) return;
      if (e.touches.length > 1) {
        disarm();
        return;
      }
      const touch = e.touches[0];
      if (!touch) return;
      const dx = touch.clientX - sx;
      const dy = touch.clientY - sy;
      const delta = barSwipeDelta(dx, dy);
      if (delta === 0) return;
      // One step per gesture: disarm before stepping so a long drag cannot repeat.
      disarm();
      swallowUntil = Number.POSITIVE_INFINITY; // until touchend arms the timer
      if (e.target && e.target !== el) {
        startTarget = e.target;
        startTarget.addEventListener("touchend", onEnd, { passive: true });
        startTarget.addEventListener("touchcancel", onCancel, { passive: true });
      }
      if (!optsRef.current.step(delta)) {
        // At the end of the list: a short buzz where the device has one.
        if (typeof navigator.vibrate === "function") navigator.vibrate(10);
      }
    };
    function onEnd() {
      unwatchTarget();
      if (swallowUntil === Number.POSITIVE_INFINITY) swallowUntil = Date.now() + CLICK_SWALLOW_MS;
      disarm();
    }
    function onCancel() {
      unwatchTarget();
      swallowUntil = 0;
      disarm();
    }
    // Capture phase: runs before React's click handlers on the buttons.
    const onClick = (e: MouseEvent) => {
      // Only the click a finger lifts into is swallowed. Keyboard / assistive-technology
      // activations (trusted, detail 0), mouse clicks, and any click once the gesture is off
      // (rotated past phone width) are real and must go through.
      const pt = (e as PointerEvent).pointerType;
      const notTouch = (pt !== undefined && pt !== "" && pt !== "touch") || (e.isTrusted && e.detail === 0);
      if (!optsRef.current.enabled || notTouch) {
        swallowUntil = 0;
        return;
      }
      if (Date.now() > swallowUntil) return;
      swallowUntil = 0;
      e.preventDefault();
      e.stopPropagation();
    };

    el.addEventListener("touchstart", onStart, { passive: true });
    el.addEventListener("touchmove", onMove, { passive: true });
    el.addEventListener("touchend", onEnd, { passive: true });
    el.addEventListener("touchcancel", onCancel, { passive: true });
    el.addEventListener("click", onClick, true);
    return () => {
      el.removeEventListener("touchstart", onStart);
      el.removeEventListener("touchmove", onMove);
      el.removeEventListener("touchend", onEnd);
      el.removeEventListener("touchcancel", onCancel);
      el.removeEventListener("click", onClick, true);
      unwatchTarget();
      disarm();
    };
  }, [ref]);
}
