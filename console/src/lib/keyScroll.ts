// Keyboard scrolling for read-oriented surfaces. Two consumers, with DIFFERENT gestures:
//   - the mirror (features/mirror) and chat (features/chat) composers call
//     scrollComposerViewport() from their textarea onKeyDown so you can scroll the
//     transcript without leaving the input: Ctrl/⌘+↑/↓ nudges by a few lines, PageUp/PageDown
//     (and Ctrl/⌘+[ / ]) page, Ctrl/⌘+End jumps to the bottom. Shift+↑/↓ is deliberately NOT
//     bound in a composer — inside a textarea that is the platform's select-by-line gesture,
//     which we must leave alone.
//   - the global dispatcher (features/keys) uses isScrollGesture() + findScroller() to
//     scroll the ACTIVE read-only viewer pane (file / diff / scm / …). There the focus is not
//     in a text field, so Shift+↑/↓ (line) and Ctrl/⌘+↑/↓ (page) keep their long-standing
//     meaning — see paneScrollDelta below.
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

// `onBottom` lets a caller own the jump-to-bottom (the mirror re-arms auto-follow and hides
// its jump button there); without it we just clamp to the end ourselves.
export function scrollComposerViewport(e: ScrollKey, el: HTMLElement | null, onBottom?: () => void): boolean {
  if (!el) return false;
  if (e.shiftKey) return false; // Shift+arrows / Shift+PageUp stay text selection in a textarea
  const mod = e.ctrlKey || e.metaKey;
  const max = Math.max(0, el.scrollHeight - el.clientHeight);

  if (mod && e.key === "End") {
    e.preventDefault();
    if (onBottom) onBottom();
    else el.scrollTop = max;
    return true;
  }

  let delta: number | null = null;
  if (mod && e.key === "ArrowUp") delta = -LINE;
  else if (mod && e.key === "ArrowDown") delta = LINE;
  else if (mod && e.key === "[") delta = -pageStep(el);
  else if (mod && e.key === "]") delta = pageStep(el);
  else if (!mod && e.key === "PageUp") delta = -pageStep(el);
  else if (!mod && e.key === "PageDown") delta = pageStep(el);
  if (delta === null) return false;

  // Consume even at an edge so an unmovable Ctrl+↑ can't fall through to history recall.
  e.preventDefault();
  el.scrollTop = Math.max(0, Math.min(max, el.scrollTop + delta));
  return true;
}

interface ScrollNavKey {
  key: string;
  shiftKey: boolean;
  ctrlKey: boolean;
  metaKey: boolean;
  altKey: boolean;
}

// Keys that could scroll a viewer pane. A cheap pre-filter so the dispatcher skips the
// (layout-reading) findScroller() call on ordinary typing. Alt is excluded so it never
// collides with the Alt+[ / ] pane-navigation chords.
const NAV_KEYS = new Set(["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " ", "[", "]"]);
export function isScrollKey(e: ScrollNavKey): boolean {
  return !e.altKey && NAV_KEYS.has(e.key);
}

// The scroll amount (px) a key should move a viewer pane's scroller, or null if it isn't a
// scroll key here. Home/End use ±1e9 (clamped to the ends by the caller).
//   - Modified gestures (Shift+↑/↓ line, Ctrl/⌘+↑/↓ & Ctrl/⌘+[ / ] page) drive the ACTIVE
//     pane regardless of DOM focus — same as before.
//   - Plain nav keys (↑/↓ line, PageUp/Down & Space page, Home/End ends) only when the
//     scroller itself is the focused element (`scrollerFocused`), so a focused button/link
//     inside the view — or the rail owning the arrows — keeps its keys.
export function paneScrollDelta(e: ScrollNavKey, el: HTMLElement, allowPlain: boolean): number | null {
  if (e.altKey) return null;
  const mod = e.ctrlKey || e.metaKey;
  const page = pageStep(el);
  const k = e.key;
  if (!mod && e.shiftKey && k === "ArrowUp") return -LINE;
  if (!mod && e.shiftKey && k === "ArrowDown") return LINE;
  if (mod && !e.shiftKey && (k === "ArrowUp" || k === "[")) return -page;
  if (mod && !e.shiftKey && (k === "ArrowDown" || k === "]")) return page;
  if (allowPlain && !mod) {
    if (k === "ArrowUp") return -LINE;
    if (k === "ArrowDown") return LINE;
    if (k === "PageUp") return -page;
    if (k === "PageDown") return page;
    if (k === " ") return e.shiftKey ? -page : page; // Space pages down, Shift+Space up
    if (k === "Home") return -1e9;
    if (k === "End") return 1e9;
  }
  return null;
}

// Find a pane's primary scrollable content element: the visible descendant with the most
// vertical overflow. Read-only viewer panes (file / diff / scm / commit / doc / …) each use a
// different scroller class, so we detect by geometry rather than hard-coding selectors — one
// rule covers every current and future viewer. Called only on an actual scroll-gesture
// keypress (rare, and auto-repeat is coalesced by the browser), so the layout reads are cheap.
// Pane content kinds whose body is read-only scrollable content — the keyboard-scroll
// gesture drives them (dispatcher) and their scroller is auto-focused on open (Pane), so the
// arrow keys scroll immediately. Terminal panes own their arrows; chat/mirror focus a composer.
export const SCROLLABLE_KINDS = new Set(["file", "diff", "wtdiff", "scm", "changes", "commit", "read", "doc"]);

// The subset that is PURE read-only scroll — no interactive keyboard handling of its own (scm
// / changes rove their commit graph & lists with the arrows). Only these get the plain-arrow /
// PageUp-Down / Home-End scroll and the auto-focus-on-open, so the interactive views keep their
// keys; every SCROLLABLE_KIND still scrolls via the modified gestures (Shift/Ctrl+↑↓).
export const VIEWER_KINDS = new Set(["file", "diff", "wtdiff", "commit", "read", "doc"]);

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
