// Remember the mirror's scroll position per session and restore the same content on return.
//
// Why not simply store px (scrollTop): almost the whole height of the transcript settles late
// (MarkdownView writes innerHTML in a passive effect, then highlight -> math -> mermaid -> image
// decode -> web fonts). On top of that the tail window is re-taken on revisit, so the same px is
// not guaranteed to point at the same content. So a turn ([data-turn-idx]) is used as the anchor,
// and what is stored is "which turn's top edge sat how many px below the viewport's top edge".
//
// Only pure DOM functions live here (no store or React imports). Switching sessions on a phone can
// only be exercised on a real device, whereas the position arithmetic itself can be run through
// every case in jsdom.

/** A saved scroll position. With atBottom, do not restore - land at the tail so tail-following is not broken. */
export interface ScrollMark {
  /** Whether the view was following the tail on leaving. true = the intent was "watching the latest", not a position. */
  atBottom: boolean;
  /** idx of the turn that overlapped the top of the viewport. */
  idx: number;
  /** That turn's top edge minus the viewport's top edge (px). Negative when scrolled into the turn. */
  offset: number;
}

/** Synthetic turns for optimistic echo / queued prompts (MirrorView assigns 1e9 and up). By the
 * time the user returns they have been replaced by real turns and the idx is gone, so they are
 * never used as an anchor. */
const SYNTHETIC_IDX = 1e9;

/** Session name -> the position last viewed. Kept only while the tab lives (a reload clears it, so
 * the next visit lands at the tail). Module-scope state, like echoStore. */
const marks = new Map<string, ScrollMark>();

export function saveMark(session: string, mark: ScrollMark | null): void {
  if (!session) return;
  if (mark) marks.set(session, mark);
  else marks.delete(session);
}

export function loadMark(session: string): ScrollMark | null {
  return (session && marks.get(session)) || null;
}

/** For tests. */
export function clearMarks(): void {
  marks.clear();
}

/** Capture the current position. The reference is the first turn overlapping the top edge of the
 * scroll container el. When no turn overlaps (empty transcript) or only synthetic turns do, return
 * null = leave it to land at the tail. */
export function captureMark(el: HTMLElement | null, atBottom: boolean): ScrollMark | null {
  if (!el) return null;
  const top = el.getBoundingClientRect().top;
  const turns = el.querySelectorAll<HTMLElement>("[data-turn-idx]");
  for (const turn of Array.from(turns)) {
    const r = turn.getBoundingClientRect();
    // The first turn extending below the top edge = the turn visible at the very top of the view.
    if (r.bottom <= top + 1) continue;
    const idx = Number(turn.getAttribute("data-turn-idx"));
    if (!Number.isFinite(idx) || idx >= SYNTHETIC_IDX) return null;
    return { atBottom, idx, offset: Math.round(r.top - top) };
  }
  return null;
}

/** The scrollTop that puts the top edge of turn idx offset px below the viewport's top edge.
 * null when that turn is not mounted (outside the tail window; the caller falls back to the tail). */
export function scrollTopForTurn(el: HTMLElement | null, idx: number, offset = 0): number | null {
  if (!el) return null;
  const turn = el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`);
  if (!turn) return null;
  const delta = turn.getBoundingClientRect().top - el.getBoundingClientRect().top - offset;
  const max = Math.max(0, el.scrollHeight - el.clientHeight);
  return Math.min(max, Math.max(0, el.scrollTop + delta));
}

/** Restore the saved position. true when restored (false when the anchor turn is missing). */
export function applyMark(el: HTMLElement | null, mark: ScrollMark): boolean {
  const top = scrollTopForTurn(el, mark.idx, mark.offset);
  if (top === null || !el) return false;
  el.scrollTop = top;
  return true;
}
