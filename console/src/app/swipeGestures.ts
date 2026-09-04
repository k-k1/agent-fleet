// Touch horizontal-swipe recognition — opening/closing the left pane, and rotating
// through running sessions on a phone (left = next, right = previous).
//
// The recognition logic lives here rather than in an App.tsx useEffect so it can be
// tested: phone gestures can only be tried on a real device, but the decisions themselves
// (which direction, which distance, which state fires what) run in jsdom given nothing
// but window events. Screen-state reads and side effects are injected via SwipeSurfaces,
// so this module imports no store.
//
// Rules:
// - Phone (<=760px): the left pane is an off-canvas drawer. Closed -> swipe right from
//   the left third opens it; open -> swipe left closes it. While the drawer is closed a
//   horizontal swipe advances/rewinds the running session by one.
// - A rightward swipe starting at the left edge belongs to the drawer (that is where the
//   rail is pulled out from). In the ordering below it settles on the 50px drawer branch
//   first and never reaches the 70px rotate threshold, so going back with a rightward
//   swipe only works when the gesture starts away from the edge.
// - Tablet (>760px and touch): the same edge swipe shows/hides the desktop rail as an
//   overlay. Mouse machines emit no TouchEvent, so this is inert there.
// - Vertical wins (|dx| <= |dy| is ignored) so scrolling is never stolen. Passing the
//   long-press window (500ms) cancels the candidate, so dragging the mirror's selection
//   handles cannot turn into an edge swipe.
import { swipeBlocked } from "./swipeGuard.ts";

/** Screen-state reads and side effects (App.tsx wires these to the store). */
export interface SwipeSurfaces {
  /** Is this a phone width (<=760px)? */
  phone(): boolean;
  /** Is this a touch-first device? Used to spot a tablet when not at phone width. */
  coarse(): boolean;
  /** Is a modal up? While one is, the rail behind it must stay untouched. */
  modal(): boolean;
  drawerOpen(): boolean;
  railOpen(): boolean;
  /** May sessions be rotated here? False in a popped-out tab. */
  rotatable(): boolean;
  setDrawer(open: boolean): void;
  openRailOverlay(): void;
  closeRail(): void;
  /** Advance the running session by delta (left swipe = +1 next, right = -1 previous). */
  rotateSession(delta: number): void;
}

/** Horizontal travel needed to open or close the rail. */
export const SWIPE_DIST = 50;
/** Horizontal travel needed to switch session. Longer than the rail threshold because
 * the whole screen changes and undoing it costs the user work, so a small sideways
 * wobble while skim-reading must not fire it. */
export const ROTATE_DIST = 70;
/** Past this, the candidate gesture is cancelled (the browser's long-press window). */
export const LONG_PRESS_MS = 500;

/** Attach gesture recognition to win. The returned function detaches it (survives
 * StrictMode's double invocation). */
export function installSwipeGestures(win: Window, s: SwipeSurfaces): () => void {
  let sx = 0,
    sy = 0,
    mode: "open" | "close" | null = null,
    // Which surface this gesture drives, settled at touchstart: the phone drawer, or
    // the tablet's desktop rail (as an overlay).
    drawer = false,
    // May a left swipe rotate sessions? Also settled at touchstart.
    rotate = false,
    longPressTimer: number | null = null;

  const cancelGesture = () => {
    mode = null;
    rotate = false;
    if (longPressTimer !== null) {
      win.clearTimeout(longPressTimer);
      longPressTimer = null;
    }
  };

  // The local is named `touch`, not `t`, so it cannot shadow the i18n `t`.
  const onStart = (e: TouchEvent) => {
    const touch = e.touches[0];
    cancelGesture();
    const phone = s.phone();
    // Above phone width this is only active on touch devices (tablets); a mouse machine
    // has no use for an edge-swipe rail.
    const tablet = !phone && s.coarse();
    drawer = phone;
    if (touch && (phone || tablet) && !s.modal()) {
      const isOpen = phone ? s.drawerOpen() : s.railOpen();
      if (isOpen) mode = "close";
      else if (touch.clientX < Math.min(win.innerWidth * 0.33, 160)) mode = "open";
      rotate = phone && !isOpen && s.rotatable() && !swipeBlocked(e.target);
    }
    if (touch) {
      sx = touch.clientX;
      sy = touch.clientY;
      if (mode || rotate) {
        longPressTimer = win.setTimeout(cancelGesture, LONG_PRESS_MS);
      }
    }
  };

  const onMove = (e: TouchEvent) => {
    if (!mode && !rotate) return;
    const touch = e.touches[0];
    if (!touch) return;
    const dx = touch.clientX - sx;
    const dy = touch.clientY - sy;
    if (Math.abs(dx) <= Math.abs(dy)) return;
    if (mode === "open" && dx > SWIPE_DIST) {
      if (drawer) s.setDrawer(true);
      else s.openRailOverlay();
      cancelGesture();
    } else if (mode === "close" && dx < -SWIPE_DIST) {
      if (drawer) s.setDrawer(false);
      else s.closeRail();
      cancelGesture();
    } else if (rotate && dx < -ROTATE_DIST) {
      // A left swipe starting at the edge coexists with mode==="open" (which waits for
      // a rightward swipe), but direction lands it here, so the two never contend.
      s.rotateSession(1);
      cancelGesture();
    } else if (rotate && dx > ROTATE_DIST) {
      // Only reached when the gesture did not start at the left edge; there the drawer
      // branch above settles first, at 50px.
      s.rotateSession(-1);
      cancelGesture();
    }
  };

  const onEnd = () => cancelGesture();
  win.addEventListener("touchstart", onStart, { passive: true });
  win.addEventListener("touchmove", onMove, { passive: true });
  win.addEventListener("touchend", onEnd, { passive: true });
  win.addEventListener("touchcancel", onEnd, { passive: true });
  return () => {
    win.removeEventListener("touchstart", onStart);
    win.removeEventListener("touchmove", onMove);
    win.removeEventListener("touchend", onEnd);
    win.removeEventListener("touchcancel", onEnd);
    cancelGesture();
  };
}
