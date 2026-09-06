import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, RefObject } from "react";
import { searchHistory, stepPos } from "../historySearch.ts";

/**
 * Ctrl+R history search in the mirror composer — bash's reverse-i-search over the prompts the user
 * sent in this conversation (the same list ↑/↓ recall walks, so what is searchable is exactly what
 * is recallable).
 *
 * Shape: the query is typed into a small input of its own rather than into the textarea. That is
 * not a shortcut — the composer is used in Japanese, and an IME needs a real field to compose in,
 * with the arrow keys and Enter left to the candidate window (hence every handler here bails on
 * isComposing). The match is previewed by writing it into the composer itself, so a multi-line
 * prompt is readable in full before it is accepted; the draft the user had is restored on cancel.
 *
 * Keys, once open: Ctrl+R / ↑ go further back, Ctrl+S / ↓ come forward, Enter (or Tab) accepts into
 * the composer WITHOUT sending, Esc and Ctrl+G cancel. Accepting rather than sending is the one
 * deliberate departure from bash, where Enter runs the command: a prompt here costs a turn, and an
 * unreviewed one that was merely being searched for is not worth that.
 *
 * Reads composerLocked, so call this after the composer's setup has decided it.
 */
export function useHistorySearch({
  history,
  draft,
  setDraft,
  setHistIdx,
  inputRef,
  composerLocked,
}: {
  /** The user's own prompts, oldest last (MirrorView's `history`). */
  history: string[];
  draft: string;
  setDraft: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  composerLocked: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // Position in the match list, or -1 for "nothing previewed yet" — see stepPos.
  const [pos, setPos] = useState(-1);
  const savedDraft = useRef(""); // the composer's text when the search opened (restored on cancel)
  const queryRef = useRef<HTMLInputElement>(null);
  // Closing is decided inside handlers that also move focus, and the blur that follows must not
  // re-enter and undo them; `open` state is too late for that, so liveness is tracked in a ref.
  const openRef = useRef(false);

  const matches = useMemo(() => (open ? searchHistory(history, query) : []), [open, history, query]);
  const cur = pos >= 0 && pos < matches.length ? matches[pos] : -1;

  const close = () => {
    openRef.current = false;
    setOpen(false);
    setQuery("");
    setPos(-1);
  };
  const focusComposer = () => {
    const el = inputRef.current;
    if (!el) return;
    el.focus();
    const end = el.value.length;
    el.setSelectionRange(end, end);
  };

  /** Preview one match by writing it into the composer (bash fills the line the same way). */
  const preview = (idx: number) => {
    if (idx >= 0) setDraft(history[idx]);
  };

  const openSearch = () => {
    if (composerLocked || !history.length) return;
    savedDraft.current = draft;
    openRef.current = true;
    setOpen(true);
    setQuery("");
    setPos(-1);
    // The field is rendered by this same update, so focus in the next frame.
    requestAnimationFrame(() => queryRef.current?.focus());
  };

  const step = (dir: 1 | -1) => {
    const next = stepPos(pos, dir, matches.length);
    setPos(next);
    preview(matches[next] ?? -1);
  };

  const onQuery = (v: string) => {
    setQuery(v);
    // Typing re-searches from the newest match, as bash does; a query that matches nothing leaves
    // the last preview standing (only the bar changes, to "no match").
    const m = searchHistory(history, v);
    const next = m.length ? 0 : -1;
    setPos(next);
    preview(m[0] ?? -1);
  };

  /** Keep what is previewed, hand the composer back. */
  const accept = () => {
    if (!openRef.current) return;
    // Continue ↑/↓ recall from the accepted entry, exactly as if it had been reached with ↑.
    if (cur >= 0) setHistIdx(cur);
    close();
    focusComposer();
  };

  /** Drop the search and put the user's own text back. */
  const cancel = () => {
    if (!openRef.current) return;
    close();
    setDraft(savedDraft.current);
    setHistIdx(null);
    focusComposer();
  };

  // The composer can be taken away mid-search (a question or a plan arrives and locks it, the
  // session is switched, the transcript is still loading). Restoring the draft here would fight
  // whatever replaced it, so just leave the search.
  useEffect(() => {
    if (open && (composerLocked || !history.length)) close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [composerLocked, history.length]);

  /**
   * Composer keys: Ctrl+R opens the search. Ctrl only, never ⌘ — on macOS ⌘R is reload and taking
   * it would cost more than it gives, while Ctrl+R is what the muscle memory being served presses
   * anyway. Reload is a page-preventable shortcut, unlike Ctrl+T/W/N (measured in this image's
   * Chromium: the same synthesized Ctrl+R reloads without preventDefault and does not with it),
   * and with no history at all this returns false, so reload still works in a conversation that
   * has nothing to search. Returns true when the key was taken (the caller stops there).
   */
  const handleKeyDown = (e: RKeyboardEvent): boolean => {
    if (e.nativeEvent.isComposing || !e.ctrlKey || e.metaKey || e.altKey) return false;
    if (e.key !== "r" && e.key !== "R") return false;
    if (composerLocked || !history.length) return false;
    e.preventDefault();
    openSearch();
    return true;
  };

  /** Keys inside the query field. */
  const onQueryKeyDown = (e: RKeyboardEvent) => {
    if (e.nativeEvent.isComposing) return; // IME candidate window owns ↑↓ and Enter
    const ctrl = e.ctrlKey && !e.metaKey && !e.altKey;
    if ((ctrl && (e.key === "r" || e.key === "R")) || (!e.ctrlKey && !e.metaKey && !e.altKey && e.key === "ArrowUp")) {
      e.preventDefault();
      step(1);
      return;
    }
    // Ctrl+S is bash's forward search. The browser's "save page" is preventable, and it is only
    // taken while this field is open.
    if ((ctrl && (e.key === "s" || e.key === "S")) || (!e.ctrlKey && !e.metaKey && !e.altKey && e.key === "ArrowDown")) {
      e.preventDefault();
      step(-1);
      return;
    }
    if (e.key === "Enter" || e.key === "Tab") {
      e.preventDefault();
      accept();
      return;
    }
    // Esc, and bash's own abort. Stop the event here: the Console's Esc layer would otherwise read
    // it as "close the pane's find bar / the pending card" behind us.
    if (e.key === "Escape" || (ctrl && (e.key === "g" || e.key === "G"))) {
      e.preventDefault();
      e.stopPropagation();
      cancel();
    }
  };

  return {
    open,
    query,
    queryRef,
    /** Match count, and the 1-based position within it (0 while nothing is previewed). */
    count: matches.length,
    pos: pos + 1,
    /** A query that matches nothing — bash's "failed reverse-i-search". */
    failed: open && query !== "" && matches.length === 0,
    canOpen: !composerLocked && history.length > 0,
    handleKeyDown,
    openSearch,
    onQuery,
    onQueryKeyDown,
    /** Focus leaving the field ends the search, keeping what is previewed (a click elsewhere, the
     *  ✕ in the bar, tapping the transcript). Cancelling stays explicit: Esc. */
    onQueryBlur: accept,
    accept,
    cancel,
  };
}
