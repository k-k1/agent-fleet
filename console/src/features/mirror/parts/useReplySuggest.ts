import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, RefObject } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { useDragScroll } from "../../../lib/dragScroll.ts";
import { setSetting } from "../../../lib/settings.ts";
import type { Settings } from "../../../lib/settings.ts";
import {
  rankQuickReplies,
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  pinQuickReply,
  unpinQuickReply,
  isQuickReplyPinned,
  quickReplyKey,
} from "../../../lib/quickReplies.ts";
import {
  stepSuggestCycle,
  suggestFilterDraft,
  cycledSuggestion,
  type SuggestCycle,
} from "../../../lib/suggestCycle.ts";
import { useChipMenu } from "../SuggestChipMenu.tsx";
import type { SuggestChip } from "./SuggestRow.tsx";

const q = encodeURIComponent;

/**
 * Reply suggestions (lib/quickReplies plus the v2 LLM candidates): the chip row itself, its
 * keyboard ring, the learning edits (forget / pin) and the on-demand ✨ fetch.
 *
 * Call it after `lastReplyText` has settled — that is the context the candidates come from.
 */
export function useReplySuggest({
  session,
  settings,
  draft,
  setDraft,
  setHistIdx,
  inputRef,
  composerLocked,
  modSend,
  lastReplyText,
  send,
  toast,
  wsDown,
}: {
  session: string;
  settings: Settings;
  draft: string;
  setDraft: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  composerLocked: boolean;
  /** Is the send key configured as Ctrl/⌘+Enter? Enter on a chip follows the same setting. */
  modSend: boolean;
  lastReplyText: string;
  send: (override?: string) => Promise<void>;
  toast: (m: string) => void;
  /** Returns true if the Workspace is stopped, and shows a toast as a side effect. */
  wsDown: () => boolean;
}) {
  // Reply suggestions v2: the contextual LLM candidates fetched by the ✨ button (merged into the
  // Layer A chip row) and the in-flight flag.
  const [llmSuggestions, setLlmSuggestions] = useState<string[]>([]);
  const [suggesting, setSuggesting] = useState(false);
  // The Tab completion cycle while typing (lib/suggestCycle). null = not cycling.
  const [cycle, setCycle] = useState<SuggestCycle | null>(null);
  const suggestRef = useRef<HTMLDivElement>(null); // the chip row (Tab moves focus here)
  // Scroll the single-line candidate row horizontally with a mouse drag or a vertical wheel; a
  // swipe keeps its default behaviour. The return value goes on the chip row's ref: the row comes
  // and goes with a conditional render, so a plain ref object would leave a returning element
  // without listeners (see the note in dragScroll.ts).
  const attachSuggestRow = useDragScroll(suggestRef);
  // The menu opened by right-click / long tap / the Menu key on a chip (pin, delete).
  const chipMenu = useChipMenu();


  // A suggestion chip: an ordinary click inserts it into the composer (edit, then Enter), and
  // ⌥/Alt-click sends immediately. On insertion the caret goes to the end and takes focus.
  const applySuggestion = (text: string, immediate: boolean) => {
    if (composerLocked) return;
    if (immediate) {
      void send(text);
      return;
    }
    setDraft(text);
    setHistIdx(null);
    // On a phone, focusing the textarea after a chip insertion opens GBoard and covers the
    // screen. On touch devices we do not focus (no keyboard), leaving the user free to send or to
    // tap and edit.
    if (coarsePointer()) {
      inputRef.current?.blur(); // fold away a keyboard that was already open
      return;
    }
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  };

  // The menu's "forget this suggestion": clear the learning AND push it onto the hidden list,
  // because deleting alone lets seeding or re-learning bring it back. A pin on it is removed too.
  // An LLM candidate (✨) is not learned, so dropping it from the current row is enough.
  const forgetSuggestion = (text: string, llm: boolean) => {
    if (llm) {
      setLlmSuggestions((prev) => prev.filter((s) => s !== text));
      return;
    }
    setSetting("quickReplies", forgetQuickReply(settings.quickReplies || {}, text));
    setSetting("quickRepliesHidden", hideQuickReply(settings.quickRepliesHidden || [], text));
    setSetting("quickRepliesPinned", unpinQuickReply(settings.quickRepliesPinned || [], text));
  };

  // The menu's pin / unpin. A pin is a stronger statement of intent than hiding, so pinning also
  // unhides, which lets a previously forgotten line be pinned again. An ✨ candidate can be pinned
  // as-is: once the user has decided they will keep using that line, there is no reason to wait
  // for the learning to catch up.
  const togglePin = (text: string) => {
    const pinned = settings.quickRepliesPinned || [];
    if (isQuickReplyPinned(pinned, text)) {
      setSetting("quickRepliesPinned", unpinQuickReply(pinned, text));
      return;
    }
    setSetting("quickRepliesPinned", pinQuickReply(pinned, text));
    setSetting("quickRepliesHidden", unhideQuickReply(settings.quickRepliesHidden || [], text));
  };

  // v2: the ✨ button — hand the recent conversation log to a one-shot headless LLM, fetch reply
  // candidates that fit the context and merge them into the chip row (session_suggest_reply.go).
  // On-demand: tokens are spent only when it is pressed.
  const fetchLlmSuggestions = async () => {
    if (!session || suggesting || wsDown()) return;
    setSuggesting(true);
    try {
      const j = await apiJSON(`api/sessions/${q(session)}/suggest-replies`, "POST", {});
      const list = Array.isArray(j?.suggestions) ? (j.suggestions as unknown[]).filter((x): x is string => typeof x === "string") : [];
      // The LLM sometimes returns the same line twice, and a chip's React key is derived from its
      // text, so fold the duplicates.
      setLlmSuggestions([...new Set(list)]);
      // Zero candidates means either no backend (none of claude/codex/opencode) or too little
      // conversation. Silence would look broken, so say so; the Layer A chips stay as they are.
      if (!list.length) toast(tr("mirror.suggest_none"));
    } catch {
      toast(tr("mirror.suggest_failed")); // generation failed (including the feature being off) — learned chips stay
    } finally {
      setSuggesting(false);
    }
  };

  // The focus ring for the suggestions = the ✨ button plus the candidate chips, in DOM order. ✨
  // is part of the cycle; Enter on it is the button's default click, i.e. the LLM fetch.
  const suggestRing = (): HTMLButtonElement[] =>
    Array.from(suggestRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);

  // The chip row scrolls on one line, so candidates past the edge are off screen. Keyboard
  // movement therefore scrolls horizontally by the minimum needed to keep the focus target
  // visible. focus()'s default scrolling also moves vertically and jumps the transcript, so it is
  // killed with preventScroll and replaced by scrollIntoView at inline/block:nearest.
  const focusRingItem = (el: HTMLButtonElement) => {
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  };

  // Movement inside the ring. Tab/Shift+Tab cycles through "candidates + input": past either end
  // it returns to the input, i.e. input -> chip 1 -> chip 2 -> input. Left/Right cycles among the
  // candidates only, and Escape goes to the input. Returns true when handled, and the caller then
  // stops.
  const onSuggestNav = (e: RKeyboardEvent<HTMLButtonElement>): boolean => {
    if (e.nativeEvent.isComposing) return false;
    if (e.key === "Escape") {
      e.preventDefault();
      inputRef.current?.focus();
      return true;
    }
    const ring = suggestRing();
    const i = ring.indexOf(e.currentTarget);
    if (i < 0 || !ring.length) return false;
    if (e.key === "Tab") {
      e.preventDefault();
      const next = e.shiftKey ? i - 1 : i + 1;
      if (next < 0 || next >= ring.length) inputRef.current?.focus(); // past an end: back to the input
      else focusRingItem(ring[next]);
      return true;
    }
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const d = e.key === "ArrowRight" ? 1 : -1;
      focusRingItem(ring[(i + d + ring.length) % ring.length]); // Left/Right wraps within the candidates
      return true;
    }
    return false;
  };

  // Keys on a chip. Movement is delegated to onSuggestNav; Enter and Ctrl(⌘)+Enter follow the
  // composer's send-key setting: under modSend (Ctrl+Enter sends) mod+Enter sends and plain Enter
  // inserts, and under enter mode (Enter sends) it is the other way round.
  const onSuggestKeyDown = (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => {
    if (onSuggestNav(e)) return;
    if (chipMenu.onKeyDown(e, text, llm)) return; // Menu key / Shift+F10 opens the pin/delete menu
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    e.preventDefault(); // do not double-fire with the button's default click, which inserts
    applySuggestion(text, modSend ? mod : !mod);
  };

  // Tab in the input: enter the chip row, or run the completion cycle. Returns true when handled.
  const handleKeyDown = (e: RKeyboardEvent): boolean => {
    // With an empty input, Tab enters the suggestions (input -> chip 1 -> chip 2 -> input). Plain
    // Tab starts at the first candidate CHIP, skipping the leading ✨, which Shift+Tab can still
    // reach. Shift+Tab runs backwards, so it enters at the end of the ring. With text present,
    // Tab keeps its normal behaviour.
    if (e.key === "Tab" && !e.nativeEvent.isComposing && draft === "") {
      const ring = suggestRing();
      const target = e.shiftKey
        ? ring[ring.length - 1]
        : suggestRef.current?.querySelector<HTMLButtonElement>(".mirror-suggest-chip");
      if (target) {
        e.preventDefault();
        focusRingItem(target);
        return true;
      }
    }
    // Tab while typing runs a shell-style completion cycle over the candidates: the ones that
    // prefix-match what was typed — the same ones visible in the chip row — are put into the input
    // in turn, and after a full lap it returns to what the user actually typed. Shift+Tab goes
    // backwards. With nothing to complete it does nothing and falls through to a normal Tab.
    if (e.key === "Tab" && !e.nativeEvent.isComposing && draft !== "" && !composerLocked) {
      const next = stepSuggestCycle(cycle, draft, suggestChips.map((c) => c.text), e.shiftKey);
      if (next) {
        e.preventDefault();
        setCycle(next);
        setDraft(next.text);
        setHistIdx(null);
        // Some browsers move the caret (leaving it at the start) when the value is replaced, so
        // put it back at the end.
        requestAnimationFrame(() => {
          const el = inputRef.current;
          if (el) el.setSelectionRange(el.value.length, el.value.length);
        });
        return true;
      }
    }
    return false;
  };

  // Reply suggestions (lib/quickReplies): the final text of the latest answer is the context for
  // the B-1 heuristic, combined with the frequency learning (settings.quickReplies).
  // During a Tab completion cycle the filter key is frozen to what the USER typed: completion has
  // already replaced the input with a candidate, so passing that through would shrink the chip row
  // to one entry and break the cycle.
  const suggestDraft = suggestFilterDraft(cycle, draft);
  const cycledText = cycledSuggestion(cycle, draft); // the candidate currently in the input, for emphasis
  const learned = settings.quickRepliesEnabled
    ? rankQuickReplies(settings.quickReplies || {}, {
        draft: suggestDraft,
        lastReply: lastReplyText,
        locale: settings.locale,
        hidden: settings.quickRepliesHidden || [],
        pinned: settings.quickRepliesPinned || [],
        limit: 20, // the chip row scrolls horizontally, so what does not fit is still reachable (pins are separate)
      })
    : [];
  // Merge the v2 LLM candidates first and the Layer A learned ones after, folding duplicates; the
  // llm flag drives the different appearance. Duplicates are decided with the same folding as the
  // learning key: case and whitespace, plus full-width vs half-width.
  const llmSet = new Set(llmSuggestions.map((s) => quickReplyKey(s)));
  const suggestChips: SuggestChip[] = [
    ...llmSuggestions.map((text) => ({ text, llm: true })),
    ...learned.filter((s) => !llmSet.has(quickReplyKey(s))).map((text) => ({ text, llm: false })),
  ];
  // Bring the candidate being walked by Tab completion into view if it is off the end of the
  // single-line chip row. Focus stays in the input, so this is scrollIntoView only, horizontally
  // and by the minimum.
  useEffect(() => {
    if (!cycledText) return;
    const el = suggestRef.current?.querySelector<HTMLElement>(".mirror-suggest-chip.cycling");
    // In Chrome 150 scrollIntoView returns a Promise, and an implicit return would hand it to
    // React as the effect's cleanup and crash. Always discard it inside a block body
    // (effect-implicit-return).
    if (el) {
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }, [cycledText]);
  // As the conversation moves on (a new answer arrives) old LLM candidates fall behind the
  // context, so they are dropped on a change of latest answer and on a session switch. Placed
  // after lastReplyText settles to avoid a TDZ on the dependency.
  useEffect(() => {
    setLlmSuggestions([]);
  }, [session, lastReplyText]);

  return {
    chips: suggestChips,
    cycledText,
    suggesting,
    rowRef: attachSuggestRow,
    chipMenu,
    applySuggestion,
    forgetSuggestion,
    togglePin,
    fetchLlmSuggestions,
    onNav: onSuggestNav,
    onChipKeyDown: onSuggestKeyDown,
    handleKeyDown,
  };
}
