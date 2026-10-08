// Page older history in until a given turn is mounted — the part of an explicit jump (a
// past-session search hit, ADR 0110) that the scroll mark cannot do: the mark only restores a turn
// that is already on screen, and the mirror mounts the tail window alone.
//
// A turn's idx is its position in the transcript, but NOT in the unit the paging cursor counts:
// claude's idx is the jsonl line number, the cursor `firstLine` is the same line number, whereas a
// store-backed agent pages by array position while its turns carry the source event's idx (sparse,
// and never below the position). So reaching is judged on the oldest idx actually HELD, and the
// cursor is only used to ask for a page. `before=firstLine&limit=N` is sized by the idx gap, which
// is exact for claude and an over-estimate (bounded by the server's clamp) for the others.
//
// Bounded on purpose: every page is a request, and every mounted turn is DOM. Past the cap the jump
// is given up with a message rather than walking a very long transcript on a click.

/** Most positions one jump may page back over; beyond it the jump is refused before any request. */
export const REACH_MAX_LINES = 8000;
/** Most requests one jump may make. */
export const REACH_MAX_PAGES = 4;
/** The server's largest window per request (clampWindowLimit). */
export const REACH_PAGE_LINES = 4000;
/** The server's smallest window per request (clampWindowLimit). */
export const REACH_MIN_PAGE = 50;
/** Positions to mount beyond the hit, so the block that holds it starts inside the window. */
export const REACH_MARGIN = 50;

export type ReachOutcome = "reached" | "mounted" | "too-far" | "failed" | "cancelled";

export interface ReachDeps {
  /** The oldest turn idx held right now (Infinity when none). */
  oldestIdx: () => number;
  /** True when the cursor is at the start of the transcript: nothing older exists. */
  exhausted: () => boolean;
  /** Fetch and prepend the page of up to `limit` positions before the cursor; false when it failed. */
  page: (limit: number) => Promise<boolean>;
  /** True once the jump is moot (session switched, a newer jump, the reader took over). Checked between pages. */
  cancelled: () => boolean;
  /** Reads the cursor, to refuse a page that did not move it. */
  cursor: () => number;
}

/** Page back until `idx` (plus a margin) is held. "mounted" = it already was; nothing was fetched. */
export async function reachTurn(idx: number, deps: ReachDeps): Promise<ReachOutcome> {
  const target = Math.max(0, idx - REACH_MARGIN);
  if (deps.oldestIdx() <= target || deps.exhausted()) return "mounted";
  if (deps.oldestIdx() - target > REACH_MAX_LINES) return "too-far";
  // The budget is spent by what was asked for AND by how far the cursor really moved, whichever is
  // more: the server trims a page to ~1 MiB keeping the newest turns, so the idx gap can stay open
  // while the cursor walks on, and a sparse idx makes the gap an over-estimate of the cursor span.
  const start = deps.cursor();
  let asked = 0;
  for (let i = 0; i < REACH_MAX_PAGES; i++) {
    if (deps.cancelled()) return "cancelled";
    if (deps.oldestIdx() <= target || deps.exhausted()) return "reached";
    const before = deps.cursor();
    const left = REACH_MAX_LINES - Math.max(asked, start - before);
    if (left < REACH_MIN_PAGE) return "too-far";
    const limit = Math.min(REACH_PAGE_LINES, deps.oldestIdx() - target, left);
    asked += Math.max(limit, REACH_MIN_PAGE);
    if (!(await deps.page(limit))) return deps.cancelled() ? "cancelled" : "failed";
    if (deps.cursor() >= before) return "failed"; // no progress: never loop on a stuck cursor
  }
  if (deps.cancelled()) return "cancelled";
  return deps.oldestIdx() <= target || deps.exhausted() ? "reached" : "too-far";
}
