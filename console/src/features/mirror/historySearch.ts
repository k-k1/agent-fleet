/**
 * Incremental search over the composer's prompt history — bash's Ctrl+R, in the mirror composer.
 * This is the pure half (which entries match, and where a step lands); the state, the keys and the
 * preview live in parts/useHistorySearch.
 *
 * `history` is the array MirrorView builds from the user's own turns, oldest last. Results run
 * newest first, because that is what reverse search means: the most recent match is offered first
 * and each further Ctrl+R walks further back.
 */

/**
 * Indexes into `history` whose text contains `query`, newest first. Case-insensitive (a no-op for
 * CJK, which is most of what gets searched here, but it is what a bash user expects of ASCII).
 * An empty query matches everything: bash walks plain history when Ctrl+R is pressed with nothing
 * typed, and the count in the bar then reads as "how far back you can go".
 */
export function searchHistory(history: string[], query: string): number[] {
  const q = query.toLowerCase();
  const out: number[] = [];
  for (let i = history.length - 1; i >= 0; i--) {
    if (!q || history[i].toLowerCase().includes(q)) out.push(i);
  }
  return out;
}

/**
 * Where a step lands. `pos` is the index into the match list, or -1 for "nothing previewed yet"
 * (the state right after opening, where the draft is still the user's own text): the first step
 * older shows the newest match rather than skipping it. Clamped at both ends — bash stops there
 * too, and wrapping around would silently re-offer a prompt the user just rejected.
 */
export function stepPos(pos: number, dir: 1 | -1, count: number): number {
  if (count === 0) return -1;
  if (pos < 0) return dir > 0 ? 0 : -1;
  return Math.min(count - 1, Math.max(0, pos + dir));
}
