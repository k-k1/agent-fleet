// When a member's own home volume is short of space. Pure: the feed holds the state and this
// decides the next one, so the thresholds are testable without a clock or a DOM.
//
// A level with hysteresis, not a bare threshold: a home hovering around the line (a build
// writing and cleaning) would otherwise raise the notice on every crossing back and forth.
// It is raised on the way up and re-armed only after real headroom is back.

const GIB = 1024 ** 3;

/** Raise when less than this is free, or usage is above RAISE_PCT. */
export const RAISE_FREE_BYTES = 2 * GIB;
export const RAISE_PCT = 90;
/** Re-arm only above this much free AND below CLEAR_PCT. */
export const CLEAR_FREE_BYTES = 3 * GIB;
export const CLEAR_PCT = 85;

/** nextHomeDiskPressure returns whether the home is in pressure after this reading. */
export function nextHomeDiskPressure(prev: boolean, used: number, total: number): boolean {
  if (!(total > 0) || !(used >= 0)) return prev; // an unusable reading changes nothing
  const free = total - used;
  const pct = (used / total) * 100;
  if (prev) return !(free > CLEAR_FREE_BYTES && pct < CLEAR_PCT);
  return free < RAISE_FREE_BYTES || pct > RAISE_PCT;
}
