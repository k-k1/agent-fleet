// The fleet graph's time window, as arithmetic — pure, so the node project can pin it.
//
// This file exists because of one defect. The wheel's pan was written with the same sign
// as the drag's, and a sign error here is INVISIBLE: panning the wrong way pushes the
// window against "now", the clamp holds it there, and the figure simply does not move.
// It reads as a dead gesture, not as a reversed one (docs/log/101 §101.11). Both the
// direction and the clamp now live in one place with a test each.
//
// Two conventions, kept apart deliberately:
//   spanFactor  MULTIPLIES the span. < 1 zooms in (the ADR's buttons pass 0.5 and 2).
//               A pinch's finger ratio is the other way round, so its caller inverts it.
//   px          moves the VIEWPORT. Positive is to the right, i.e. later.

export interface TimeWindow {
  from: number;
  to: number;
}

const DAY_MS = 86_400_000;
/** 30 minutes — zooming past this stops being readable. */
export const MIN_SPAN_MS = 30 * 60_000;
/** Matches ADR 0096 decision 8's activity-retention tier. */
export const MAX_SPAN_MS = 30 * DAY_MS;

/**
 * The two invariants every gesture has to leave standing: the span stays inside
 * [MIN_SPAN_MS, MAX_SPAN_MS], and the right edge never passes `now` — a window that
 * reaches into the future scrolls into a blank the figure can never fill.
 */
export function clampWindow(from: number, to: number, now: number): TimeWindow {
  const span = Math.min(MAX_SPAN_MS, Math.max(MIN_SPAN_MS, to - from));
  const end = Math.min(now, from + span);
  return { from: end - span, to: end };
}

/**
 * Move the window by a distance in pixels, through the scale itself: the figure travels
 * exactly as far as the input did. (The first version moved a fixed 12% of the window per
 * wheel EVENT, and one trackpad flick — dozens of events — threw the window days away.)
 */
export function panByPx(win: TimeWindow, px: number, width: number, now: number): TimeWindow {
  const span = win.to - win.from;
  const shift = px * (span / Math.max(1, width));
  return clampWindow(win.from + shift, win.to + shift, now);
}

/**
 * Scale the span by `spanFactor`, keeping the instant at `fraction` of the drawable width
 * under the same pixel. That anchor is what makes a pinch feel like a map: without it the
 * window always grows from the right edge, so the moment under the fingers slides away
 * from them.
 *
 * The anchor is given up — not the clamp — when the result would reach past `now`:
 * clampWindow slides the window back, so zooming out near the right edge pulls the anchor
 * leftwards rather than opening a blank stretch of future.
 */
export function zoomAt(win: TimeWindow, spanFactor: number, fraction: number, now: number): TimeWindow {
  const span = win.to - win.from;
  const next = Math.min(MAX_SPAN_MS, Math.max(MIN_SPAN_MS, span * spanFactor));
  const f = Math.min(1, Math.max(0, fraction));
  const anchor = win.from + f * span;
  const from = anchor - f * next;
  return clampWindow(from, from + next, now);
}

/**
 * A pinch's finger-span ratio as a spanFactor. Spreading the fingers (ratio > 1) means
 * zoom IN, which is a SMALLER time span — the inversion that would otherwise be repeated
 * at the call site, which is where the wheel's sign went wrong.
 */
export const pinchSpanFactor = (ratio: number): number => (ratio > 0 ? 1 / ratio : 1);
