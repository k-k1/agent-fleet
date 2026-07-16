// Keyboard scrolling for read-oriented surfaces. Shift+↑/↓ nudges by a few lines;
// Ctrl/⌘+↑/↓ and Ctrl/⌘+[ / ] page up/down. Two consumers:
//   - the mirror (features/mirror) and chat (features/chat) composers call
//     scrollComposerViewport() from their textarea onKeyDown so you can scroll the
//     transcript without leaving the input;
//   - the global dispatcher (features/keys) uses isScrollGesture() + findScroller() to
//     scroll the ACTIVE read-only viewer pane (file / diff / scm / …) off the same keys.
//
// scrollComposerViewport returns true when it handled the event (and called preventDefault),
// so the caller can bail out before its own Enter / history logic runs. Matches brackets by
// the PRODUCED character (e.key), which is what the keycap shows on the user's layout —
// unlike JIS pane chords, which reason about physical .code.

const LINE = 48; // px per Shift+arrow — a few text lines
// Near-full viewport per page, keeping a sliver of overlap for continuity.
const pageStep = (el: HTMLElement) => Math.max(LINE, el.clientHeight - LINE);

interface ScrollKey {
  key: string;
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  preventDefault(): void;
}

export function scrollComposerViewport(e: ScrollKey, el: HTMLElement | null): boolean {
  if (!el) return false;
  const mod = e.ctrlKey || e.metaKey;
  let delta: number | null = null;
  if (!mod && e.shiftKey && e.key === "ArrowUp") delta = -LINE;
  else if (!mod && e.shiftKey && e.key === "ArrowDown") delta = LINE;
  else if (mod && !e.shiftKey && (e.key === "ArrowUp" || e.key === "[")) delta = -pageStep(el);
  else if (mod && !e.shiftKey && (e.key === "ArrowDown" || e.key === "]")) delta = pageStep(el);
  if (delta === null) return false;

  const max = el.scrollHeight - el.clientHeight;
  // Consume even at an edge so an unmovable Shift/Ctrl+↑ can't fall through to history recall.
  e.preventDefault();
  el.scrollTop = Math.max(0, Math.min(max, el.scrollTop + delta));
  return true;
}

// True when e is one of our scroll gestures (Shift+↑/↓, Ctrl/⌘+↑/↓, Ctrl/⌘+[ / ]). Lets the
// dispatcher cheaply pre-filter before the (layout-reading) findScroller() call. Alt is
// excluded so it can't collide with the Alt+[ / ] pane-navigation chords.
export function isScrollGesture(e: {
  key: string;
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
}): boolean {
  if (e.altKey) return false;
  const mod = e.ctrlKey || e.metaKey;
  if (!mod && e.shiftKey) return e.key === "ArrowUp" || e.key === "ArrowDown";
  if (mod && !e.shiftKey)
    return e.key === "ArrowUp" || e.key === "ArrowDown" || e.key === "[" || e.key === "]";
  return false;
}

// Find a pane's primary scrollable content element: the visible descendant with the most
// vertical overflow. Read-only viewer panes (file / diff / scm / commit / doc / …) each use a
// different scroller class, so we detect by geometry rather than hard-coding selectors — one
// rule covers every current and future viewer. Called only on an actual scroll-gesture
// keypress (rare, and auto-repeat is coalesced by the browser), so the layout reads are cheap.
export function findScroller(root: HTMLElement | null): HTMLElement | null {
  if (!root) return null;
  let best: HTMLElement | null = null;
  let bestOverflow = 0;
  for (const el of root.querySelectorAll<HTMLElement>("*")) {
    const overflow = el.scrollHeight - el.clientHeight;
    if (overflow <= bestOverflow || el.clientHeight < 40) continue; // too small / not the main body
    const oy = getComputedStyle(el).overflowY;
    if (oy !== "auto" && oy !== "scroll") continue;
    bestOverflow = overflow;
    best = el;
  }
  return best;
}
