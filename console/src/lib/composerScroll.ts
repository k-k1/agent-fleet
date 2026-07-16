// Scroll the transcript above a composer with the keyboard, without leaving the input.
// Shift+↑/↓ nudges by a few lines; Ctrl/⌘+↑/↓ and Ctrl/⌘+[ / ] page up/down. Shared by
// the mirror (features/mirror) and chat (features/chat) composers so both feel identical.
//
// Returns true when it handled the event (and called preventDefault), so the composer's
// own onKeyDown can bail out before its Enter / history logic runs. Matches brackets by the
// PRODUCED character (e.key), which is what the keycap shows on the user's layout — unlike
// the global dispatcher, which reasons about physical .code for JIS pane chords.

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
