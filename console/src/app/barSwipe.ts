// Pure decisions behind the activity bar's vertical swipe (ADR 0140, note of 2026-11-19).
// Kept apart from the DOM wiring (useBarSwipe.ts) so direction, threshold, clamping and the
// overflow rule can be tested without a touch device. Horizontal gestures stay in
// swipeGestures.ts; nothing here reads or changes them.

/** Vertical travel that steps one view: about one icon (the buttons are 42px tall). It also serves as the
 * movement slop: a tap that wobbles a few pixels never reaches it. */
export const BAR_STEP_DIST = 42;
/** Which way is "next": the list moves up under the finger, like a picker. The one place to
 * flip if a phone shows it feels backwards. */
export const BAR_SWIPE_UP_IS_NEXT = true;

/** How far to step for a drag of (dx, dy): +1 next, -1 previous, 0 not yet a swipe. Vertical
 * must dominate, otherwise a sideways drag (the drawer's close gesture) would also step. */
export function barSwipeDelta(dx: number, dy: number): -1 | 0 | 1 {
  if (Math.abs(dy) <= Math.abs(dx)) return 0;
  if (Math.abs(dy) < BAR_STEP_DIST) return 0;
  const up = dy < 0;
  return up === BAR_SWIPE_UP_IS_NEXT ? 1 : -1;
}

/** The view to land on after stepping delta from current, clamped at both ends (no wrap:
 * Files back to Repositories is disorienting with six entries). Null when nothing changes
 * (already at the end, or no steps). A current view not in the list starts from the first. */
export function stepView<T>(ids: readonly T[], current: T, delta: number): T | null {
  if (ids.length === 0 || delta === 0) return null;
  const i = ids.indexOf(current);
  if (i < 0) return ids[0] ?? null;
  const next = Math.min(ids.length - 1, Math.max(0, i + delta));
  return next === i ? null : (ids[next] ?? null);
}

/** May the bar claim a vertical drag? Only when its column fits: an overflowing column must
 * keep native scrolling, so the gesture would fight it. The 1px tolerance absorbs
 * sub-pixel rounding that makes a fitting column read one pixel taller. */
export function barFits(el: { scrollHeight: number; clientHeight: number }): boolean {
  return el.scrollHeight <= el.clientHeight + 1;
}
