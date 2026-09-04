import { useCallback, useEffect, useState } from "react";
import type { RefObject } from "react";

// Makes a single-line horizontal row (the reply-suggestion chip row, say) draggable sideways.
// Touch swipes are left to the native overflow-x:auto behaviour, with its inertia, so only mouse
// drags and the vertical wheel are handled here. The click that follows a drag is swallowed, so
// that grabbing a chip and flicking the row cannot insert or send a suggestion.

const DRAG_THRESHOLD = 4; // move more than this (px) and it counts as a drag, below it is a click
const LINE_PX = 16; // one deltaMode=line row in px (one wheel notch is about three rows)

type WheelLike = Pick<WheelEvent, "deltaX" | "deltaY" | "deltaMode" | "ctrlKey">;
type ScrollBox = Pick<HTMLElement, "scrollLeft" | "scrollWidth" | "clientWidth">;

// Translates the vertical wheel into a horizontal scroll amount (px). Returning 0 means "this row
// does not handle it": do not preventDefault, let it reach the parent (conversation log / pane).
//   - a row that does not overflow has nothing to grab, so it passes through
//   - a horizontally dominant delta (trackpad side swipe) is left to the native inertia
//   - Ctrl+wheel is the browser's pinch zoom
//   - at an edge, a direction that cannot move is not taken (overscroll-behavior alone makes it
//     look stuck)
export function wheelScrollDelta(e: WheelLike, el: ScrollBox): number {
  const max = el.scrollWidth - el.clientWidth;
  if (max <= 0) return 0;
  if (e.ctrlKey) return 0;
  if (Math.abs(e.deltaX) > Math.abs(e.deltaY)) return 0;
  const raw = e.deltaMode === 1 ? e.deltaY * LINE_PX : e.deltaMode === 2 ? e.deltaY * el.clientWidth : e.deltaY;
  if (!raw) return 0;
  const clamped = Math.max(-el.scrollLeft, Math.min(max - el.scrollLeft, raw));
  return Math.abs(clamped) < 1 ? 0 : clamped;
}

// Pass the returned function as the target element's `ref` (`<div ref={useDragScroll(rowRef)}>`).
//
// This must not be an effect that only reads a ref object. The row is conditionally rendered and
// leaves and re-enters the DOM while streaming (chat) or while AUQ/plan hold a lock (mirror).
// Assigning to a ref triggers neither a re-render nor an effect, so such an effect would bind its
// listeners to the first element it saw: absent on the first pass it would never bind at all, and
// it would never follow the new element that comes back, silently killing wheel scrolling. The
// element is therefore kept in state and rebound through a callback ref that sees it come and go.
// The passed ref is also written, so callers can still reach the element (querySelector etc.).
export function useDragScroll<T extends HTMLElement>(ref?: RefObject<T | null>): (el: T | null) => void {
  const [node, setNode] = useState<T | null>(null);
  const attach = useCallback(
    (el: T | null) => {
      if (ref) ref.current = el;
      setNode(el);
    },
    [ref],
  );

  useEffect(() => {
    const el = node;
    if (!el) return;
    let startX = 0;
    let startLeft = 0;
    let dragging = false;
    let moved = false;

    const onDown = (e: PointerEvent) => {
      if (e.pointerType !== "mouse" || e.button !== 0) return; // touch/pen is left to the native behaviour
      dragging = true;
      moved = false; // a new gesture starts, carrying no trace of the previous drag
      startX = e.clientX;
      startLeft = el.scrollLeft;
    };
    const onMove = (e: PointerEvent) => {
      if (!dragging) return;
      const dx = e.clientX - startX;
      if (!moved && Math.abs(dx) < DRAG_THRESHOLD) return; // a tiny movement passes through as a click
      moved = true;
      el.setPointerCapture?.(e.pointerId); // keep following even when crossing a child button
      el.scrollLeft = startLeft - dx;
      e.preventDefault(); // stop text selection while dragging
    };
    const onUp = () => {
      dragging = false;
    };
    // The vertical wheel scrolls sideways: in a narrow pane the chip row normally overflows, so
    // there has to be a way to move it without grabbing it.
    const onWheel = (e: WheelEvent) => {
      const dx = wheelScrollDelta(e, el);
      if (!dx) return; // what is not handled here is left to the parent's scrolling
      el.scrollLeft += dx;
      e.preventDefault();
    };
    // Drop the click that arrives after a drag (which would insert the chip) before it reaches a
    // child; capture gets it first.
    const onClick = (e: MouseEvent) => {
      if (!moved) return;
      moved = false;
      e.preventDefault();
      e.stopPropagation();
    };

    el.addEventListener("pointerdown", onDown);
    el.addEventListener("pointermove", onMove);
    el.addEventListener("pointerup", onUp);
    el.addEventListener("pointercancel", onUp);
    el.addEventListener("click", onClick, true);
    el.addEventListener("wheel", onWheel, { passive: false }); // preventDefault, so not passive
    return () => {
      el.removeEventListener("pointerdown", onDown);
      el.removeEventListener("pointermove", onMove);
      el.removeEventListener("pointerup", onUp);
      el.removeEventListener("pointercancel", onUp);
      el.removeEventListener("click", onClick, true);
      el.removeEventListener("wheel", onWheel);
    };
  }, [node]);

  return attach;
}
