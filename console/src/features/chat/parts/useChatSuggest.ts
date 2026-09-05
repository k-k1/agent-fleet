import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import { errText } from "../../../core/api/client.ts";
import { useDragScroll } from "../../../lib/dragScroll.ts";
import { t } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { setSetting, type Settings } from "../../../lib/settings.ts";
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
import { suggestFilterDraft, cycledSuggestion, type SuggestCycle } from "../../../lib/suggestCycle.ts";
import { useChipMenu } from "../../mirror/SuggestChipMenu.tsx";
import { chatSuggestReplies } from "../api.ts";
import { splitPastedImages } from "../../../lib/pastedImages.ts";
import type { Conversation } from "../../../types/chat.ts";

// useChatSuggest owns the reply-suggestion strip above the composer: the candidate list
// (✨ = on-demand LLM candidates merged ahead of lib/quickReplies' learned ones), the
// pin/forget menu, and the keyboard focus ring the chips live in. ChatView keeps the
// composer state itself and hands in what the suggestions have to act on.
export function useChatSuggest({
  conv,
  conversationId,
  input,
  settings,
  modSend,
  inputRef,
  isStreaming,
  setInput,
  setHistIdx,
  send,
  toast,
}: {
  conv: Conversation | null;
  conversationId: string | null;
  input: string;
  settings: Settings;
  modSend: boolean;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** While streaming or reconnecting, neither insert nor send immediately (evaluated at call time). */
  isStreaming: () => boolean;
  setInput: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  send: (override?: string) => void;
  toast: (msg: string) => void;
}) {
  // Reply suggestions v2: LLM candidates fetched by the ✨ button (merged into the Layer A chip
  // row) and the in-flight flag.
  const [llmSuggestions, setLlmSuggestions] = useState<string[]>([]);
  const [suggesting, setSuggesting] = useState(false);
  // Tab-completion cycle over the text being typed (lib/suggestCycle). null = not cycling.
  const [cycle, setCycle] = useState<SuggestCycle | null>(null);
  const suggestRef = useRef<HTMLDivElement>(null); // chip row (Tab moves focus here)
  // The single-line candidate row scrolls horizontally with mouse drag / vertical wheel (swipe keeps
  // its default behaviour). The chip row disappears and comes back while streaming, so it is attached
  // through the returned callback ref: with a ref object alone the returning element never gets the
  // listeners (see dragScroll.ts).
  const attachSuggestRow = useDragScroll(suggestRef);
  // Menu opened by right-click / long-press / the Menu key on a chip (pin, forget). Shared with MirrorView.
  const chipMenu = useChipMenu();

  // Reply suggestions (lib/quickReplies): the latest assistant message is the B-1 context, merged
  // with frequency learning.
  let chatLastReply = "";
  const msgs = conv?.messages ?? [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === "assistant") {
      chatLastReply = splitPastedImages(msgs[i].content).text.trim();
      break;
    }
  }
  // While cycling, freeze the filter key to what the user actually typed: completion has already
  // replaced the input with the candidate itself, so passing that through would thin the chip row out
  // and break the cycle.
  const suggestDraft = suggestFilterDraft(cycle, input);
  const cycledText = cycledSuggestion(cycle, input); // candidate currently in the input (for highlighting)
  const learned = settings.quickRepliesEnabled
    ? rankQuickReplies(settings.quickReplies || {}, {
        draft: suggestDraft,
        lastReply: chatLastReply,
        locale: settings.locale,
        hidden: settings.quickRepliesHidden || [],
        pinned: settings.quickRepliesPinned || [],
        limit: 20, // the chip row scrolls horizontally, so what does not fit stays reachable (pins are separate)
      })
    : [];
  // Merge v2 LLM candidates first and learned Layer A candidates after, folding duplicates; the llm
  // flag drives the styling. Duplicates fold on the same key as learning does (case and whitespace,
  // plus full-width/half-width).
  const llmSet = new Set(llmSuggestions.map((s) => quickReplyKey(s)));
  const suggestChips: { text: string; llm: boolean }[] = [
    ...llmSuggestions.map((text) => ({ text, llm: true })),
    ...learned.filter((s) => !llmSet.has(quickReplyKey(s))).map((text) => ({ text, llm: false })),
  ];
  // Once the conversation moves on (a new reply arrives) old LLM candidates are stale context, so
  // drop them on a new reply and on a conversation switch.
  useEffect(() => {
    setLlmSuggestions([]);
  }, [conversationId, chatLastReply]);
  // Scroll the candidate being cycled into view when it falls outside the single-line chip row.
  useEffect(() => {
    if (!cycledText) return;
    const el = suggestRef.current?.querySelector<HTMLElement>(".chat-suggest-chip.cycling");
    // scrollIntoView returns a Promise on Chrome 150: an implicit return would be taken as the
    // effect's cleanup and crash, so always discard it in a block body.
    if (el) {
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }, [cycledText]);
  // Suggestion chip: a plain click inserts into the composer, ⌥/Alt sends immediately (as in MirrorView).
  const applySuggestion = (text: string, immediate: boolean) => {
    if (isStreaming()) return;
    if (immediate) {
      void send(text);
      return;
    }
    setInput(text);
    setHistIdx(null);
    // Phones: focusing the textarea on insert opens GBoard and covers the screen, so on touch devices
    // we do not focus (no keyboard) - the user can send, or tap to edit.
    if (coarsePointer()) {
      inputRef.current?.blur(); // also folds away a keyboard that was already open
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
  // Menu "forget this candidate": clear the learning, add it to the hidden list and unpin it -
  // clearing alone lets seeding or re-learning bring it back. An LLM candidate (✨) is not learned, so
  // it is only removed from the current row.
  const forgetSuggestion = (text: string, llm: boolean) => {
    if (llm) {
      setLlmSuggestions((prev) => prev.filter((s) => s !== text));
      return;
    }
    setSetting("quickReplies", forgetQuickReply(settings.quickReplies || {}, text));
    setSetting("quickRepliesHidden", hideQuickReply(settings.quickRepliesHidden || [], text));
    setSetting("quickRepliesPinned", unpinQuickReply(settings.quickRepliesPinned || [], text));
  };
  // Menu "always show (pin)" / "unpin", handled as in MirrorView: a pin is a stronger statement than
  // a hide, so pinning also clears the hidden entry.
  const togglePin = (text: string) => {
    const pinned = settings.quickRepliesPinned || [];
    if (isQuickReplyPinned(pinned, text)) {
      setSetting("quickRepliesPinned", unpinQuickReply(pinned, text));
      return;
    }
    setSetting("quickRepliesPinned", pinQuickReply(pinned, text));
    setSetting("quickRepliesHidden", unhideQuickReply(settings.quickRepliesHidden || [], text));
  };
  // v2 ✨ button: hand the conversation log to a one-shot headless LLM and merge the contextual reply
  // candidates into the chip row (chat_suggest_reply.go). Not available while the conversation is
  // still a draft.
  const fetchLlmSuggestions = async () => {
    if (!conversationId || suggesting) return;
    setSuggesting(true);
    try {
      const j = await chatSuggestReplies(conversationId);
      // apiJSON resolves a server error as {error}; never let a failure turn into a "no candidates" toast.
      if (j?.error) {
        toast(errText(j.error) || t("chat.suggest_failed"));
        return;
      }
      const list = Array.isArray(j?.suggestions) ? j.suggestions.filter((x): x is string => typeof x === "string") : [];
      setLlmSuggestions(list);
      if (!list.length) toast(t("chat.suggest_none"));
    } catch {
      toast(t("chat.suggest_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  // Focus ring of the suggestion strip = ✨ button plus candidate chips, in DOM order. As in MirrorView.
  const suggestRing = (): HTMLButtonElement[] =>
    Array.from(suggestRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);

  // The chip row is one scrolling line, so keyboard moves follow horizontally only, by the minimum
  // needed; focus's default scrolling also moves vertically and jumps the transcript, hence
  // preventScroll.
  const focusRingItem = (el: HTMLButtonElement) => {
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  };

  // Movement within the ring. Tab/Shift+Tab cycles candidates plus the input (past either end returns
  // to the input); ←/→ cycles among the candidates only; Escape returns to the input. Returns true
  // when handled.
  const onSuggestNav = (e: KeyboardEvent<HTMLButtonElement>): boolean => {
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
      if (next < 0 || next >= ring.length) inputRef.current?.focus(); // past the end -> back to the input
      else focusRingItem(ring[next]);
      return true;
    }
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const d = e.key === "ArrowRight" ? 1 : -1;
      focusRingItem(ring[(i + d + ring.length) % ring.length]); // ←/→ cycles among the candidates
      return true;
    }
    return false;
  };

  // Key handling on a chip: movement goes to onSuggestNav, and Enter / Ctrl(⌘)+Enter follow the
  // composer's send-key setting - with modSend, mod+Enter sends and plain Enter inserts; in enter mode
  // the two swap.
  const onSuggestKeyDown = (e: KeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => {
    if (onSuggestNav(e)) return;
    if (chipMenu.onKeyDown(e, text, llm)) return; // Menu key / Shift+F10 -> pin/forget menu
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    e.preventDefault(); // do not double-fire with the button's default click, which inserts
    applySuggestion(text, modSend ? mod : !mod);
  };

  return {
    suggestRef,
    attachSuggestRow,
    chipMenu,
    suggesting,
    suggestChips,
    cycle,
    setCycle,
    cycledText,
    applySuggestion,
    forgetSuggestion,
    togglePin,
    fetchLlmSuggestions,
    suggestRing,
    focusRingItem,
    onSuggestNav,
    onSuggestKeyDown,
  };
}
