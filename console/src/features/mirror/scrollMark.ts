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
  /** Accept the nearest earlier mounted turn when idx itself is not one. Set by a jump to a
   * past-session search hit (ADR 0110): the index records every transcript row, while the mirror
   * mounts one block per group of rows under the group's first idx, so a hit inside a claude
   * reply has no element of its own. A mark captured here always names a mounted turn and leaves
   * this off. */
  near?: boolean;
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

/** Who wants to hear about an explicit jump: every mounted mirror, which acts only on its own session. */
const jumpListeners = new Set<(session: string, mark: ScrollMark) => void>();

/** Moves a session's view to mark — an explicit request such as a search hit, not a remembered
 * position. Saved as the session's mark so a mirror that mounts it next lands there, AND told to
 * the mirrors already showing it: those read the mark only when they switch session, so without
 * this a jump into an open session would do nothing. */
export function requestJump(session: string, mark: ScrollMark): void {
  saveMark(session, mark);
  for (const fn of jumpListeners) fn(session, mark);
}

export function onJump(fn: (session: string, mark: ScrollMark) => void): () => void {
  jumpListeners.add(fn);
  return () => {
    jumpListeners.delete(fn);
  };
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

/** Capture the position against the first turn boundary BELOW the reader instead of the block
 * they are inside. Same shape as captureMark, and the offset is >= 0 rather than <= 0.
 *
 * For the backward-paging hold this is the only reference that means the same thing afterwards.
 * The prepended rows join the block the reader is in at its FRONT (blockIdentity.ts), so that
 * block's top edge moves up by everything that was inserted — "3,752px into turn 182" then points
 * at content tens of thousands of px earlier. The top of the NEXT block does not move relative to
 * the reader: nothing is inserted between them. Returns null when the reader is inside the last
 * block (nothing below to hold on to) — the caller falls back to captureMark. */
export function captureMarkBelow(el: HTMLElement | null): ScrollMark | null {
  if (!el) return null;
  const top = el.getBoundingClientRect().top;
  for (const turn of Array.from(el.querySelectorAll<HTMLElement>("[data-turn-idx]"))) {
    const r = turn.getBoundingClientRect();
    if (r.top < top - 1) continue;
    const idx = Number(turn.getAttribute("data-turn-idx"));
    if (!Number.isFinite(idx) || idx >= SYNTHETIC_IDX) return null;
    return { atBottom: false, idx, offset: Math.round(r.top - top) };
  }
  return null;
}

/** The scrollTop that puts the top edge of turn idx offset px below the viewport's top edge.
 * null when that turn is not mounted (outside the tail window; the caller falls back to the tail).
 * With near, a missing idx resolves to the block that holds it — the nearest earlier mounted turn —
 * but never past the start of the window: a turn older than every mounted one is outside it. */
export function scrollTopForTurn(el: HTMLElement | null, idx: number, offset = 0, near = false): number | null {
  if (!el) return null;
  let turn = el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`);
  if (!turn && near) {
    // The block whose ROW RANGE holds idx: a block extended at its front by a backward page keeps
    // its original name (blockIdentity.ts), so the nearest earlier name is not where the row is.
    for (const t of Array.from(el.querySelectorAll<HTMLElement>("[data-turn-first]"))) {
      const lo = Number(t.getAttribute("data-turn-first"));
      const hi = Number(t.getAttribute("data-turn-last"));
      if (Number.isFinite(lo) && Number.isFinite(hi) && lo <= idx && idx <= hi) {
        turn = t;
        break;
      }
    }
  }
  if (!turn && near) {
    let best = -Infinity;
    for (const t of Array.from(el.querySelectorAll<HTMLElement>("[data-turn-idx]"))) {
      const n = Number(t.getAttribute("data-turn-idx"));
      if (Number.isFinite(n) && n < SYNTHETIC_IDX && n <= idx && n > best) {
        best = n;
        turn = t;
      }
    }
  }
  if (!turn) return null;
  const delta = turn.getBoundingClientRect().top - el.getBoundingClientRect().top - offset;
  const max = Math.max(0, el.scrollHeight - el.clientHeight);
  return Math.min(max, Math.max(0, el.scrollTop + delta));
}

/** Restore the saved position. true when restored (false when the anchor turn is missing). */
export function applyMark(el: HTMLElement | null, mark: ScrollMark): boolean {
  const top = scrollTopForTurn(el, mark.idx, mark.offset, mark.near);
  if (top === null || !el) return false;
  el.scrollTop = top;
  return true;
}
