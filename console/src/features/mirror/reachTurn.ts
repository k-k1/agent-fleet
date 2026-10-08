// Page older history in until a given turn is mounted — the part of an explicit jump (a
// past-session search hit, ADR 0110) that the scroll mark cannot do: the mark only restores a turn
// that is already on screen, and the mirror mounts the tail window alone.
//
// A turn's idx is its absolute position in the transcript (claude: the jsonl line number;
// store-backed agents: the turn index) and `firstLine` is the same unit — the oldest position the
// mirror holds — so "is the hit mounted" is `idx >= firstLine`, and one `before=firstLine&limit=N`
// request covers N positions. No search over cursors is needed; the only question is how far to go.
//
// Bounded on purpose: every page is a request, and every mounted turn is DOM. Past the cap the jump
// is given up with a message rather than walking a very long transcript on a click.

/** Most positions one jump may page back over; beyond it the jump is refused before any request. */
export const REACH_MAX_LINES = 8000;
/** Most requests one jump may make. */
export const REACH_MAX_PAGES = 4;
/** The server's largest window per request (clampWindowLimit). */
export const REACH_PAGE_LINES = 4000;
/** Positions to mount beyond the hit, so the block that holds it starts inside the window. */
export const REACH_MARGIN = 50;

export type ReachOutcome = "reached" | "mounted" | "too-far" | "failed" | "cancelled";

export interface ReachDeps {
  /** The oldest position held right now. */
  firstLine: () => number;
  /** Fetch and prepend the page of up to `limit` positions before firstLine; false when it failed. */
  page: (limit: number) => Promise<boolean>;
  /** True once the jump is moot (session switched, the reader took over). Checked between pages. */
  cancelled: () => boolean;
}

/** Page back until `idx` (plus a margin) is held. "mounted" = it already was; nothing was fetched. */
export async function reachTurn(idx: number, deps: ReachDeps): Promise<ReachOutcome> {
  const target = Math.max(0, idx - REACH_MARGIN);
  if (deps.firstLine() <= target) return "mounted";
  if (deps.firstLine() - target > REACH_MAX_LINES) return "too-far";
  for (let i = 0; i < REACH_MAX_PAGES; i++) {
    if (deps.cancelled()) return "cancelled";
    const before = deps.firstLine();
    if (before <= target) return "reached";
    if (!(await deps.page(Math.min(REACH_PAGE_LINES, before - target)))) return deps.cancelled() ? "cancelled" : "failed";
    if (deps.firstLine() >= before) return "failed"; // no progress: never loop on a stuck cursor
  }
  if (deps.cancelled()) return "cancelled";
  return deps.firstLine() <= target ? "reached" : "failed";
}
