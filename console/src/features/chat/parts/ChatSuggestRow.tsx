import type { KeyboardEvent, RefCallback } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { isQuickReplyPinned } from "../../../lib/quickReplies.ts";
import { useChipMenu, SuggestChipMenu } from "../../mirror/SuggestChipMenu.tsx";

// ChatSuggestRow is the one-line, horizontally scrolled strip of reply suggestions above
// the composer (✨ = on-demand LLM candidates, the rest learned by lib/quickReplies), plus
// the pin/forget menu those chips open. The ranking, the Tab-completion cycle and the
// focus ring all stay in ChatView — this draws what they decided.
export function ChatSuggestRow({
  show,
  showAiButton,
  attachSuggestRow,
  suggesting,
  onFetchLlmSuggestions,
  onSuggestNav,
  suggestChips,
  pinnedList,
  cycledText,
  chipMenu,
  onApply,
  onSuggestKeyDown,
  onTogglePin,
  onForget,
}: {
  show: boolean;
  showAiButton: boolean;
  attachSuggestRow: RefCallback<HTMLDivElement>;
  suggesting: boolean;
  onFetchLlmSuggestions: () => void;
  onSuggestNav: (e: KeyboardEvent<HTMLButtonElement>) => boolean;
  suggestChips: { text: string; llm: boolean }[];
  pinnedList: string[] | undefined;
  cycledText: string | null;
  chipMenu: ReturnType<typeof useChipMenu>;
  onApply: (text: string, immediate: boolean) => void;
  onSuggestKeyDown: (e: KeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => void;
  onTogglePin: (text: string) => void;
  onForget: (text: string, llm: boolean) => void;
}) {
  const tr = useT();
  return (
    <>
      {/* Reply suggestions: frequently used short phrases, candidates derived from the latest
          answer (Layer A), and the sparkle button's LLM candidates (v2). Click inserts;
          Option-click sends immediately. */}
      {show && (
        <div className="chat-suggest" ref={attachSuggestRow}>
          {showAiButton && (
            <button
              type="button"
              className="chat-suggest-ai"
              title={tr("chat.suggest_ai")}
              disabled={suggesting}
              onClick={onFetchLlmSuggestions}
              onKeyDown={onSuggestNav} // leave Enter to the default click (fetching candidates)
            >
              <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} />
            </button>
          )}
          {suggestChips.map((sg) => (
            // Pinned suggestions are held at the front and marked with a pin. Delete/pin
            // live in the right-click, long-press and Menu-key menu.
            <button
              key={(sg.llm ? "l:" : "a:") + sg.text}
              type="button"
              className={
                "chat-suggest-chip" +
                (sg.llm ? " llm" : "") +
                (isQuickReplyPinned(pinnedList, sg.text) ? " pinned" : "") +
                (sg.text === cycledText ? " cycling" : "") // the candidate Tab has put in the box
              }
              aria-current={sg.text === cycledText ? "true" : undefined}
              title={tr("mirror.suggest_hint")}
              onClick={(e) => {
                if (chipMenu.clickSwallowed()) return; // release of the long-press that opened the menu
                onApply(sg.text, e.ctrlKey || e.altKey || e.metaKey);
              }}
              onKeyDown={(e) => onSuggestKeyDown(e, sg.text, sg.llm)}
              {...chipMenu.chipProps(sg.text, sg.llm)}
            >
              {isQuickReplyPinned(pinnedList, sg.text) && (
                <Icon name="pinned" className="chat-suggest-pin" />
              )}
              {sg.text}
            </button>
          ))}
        </div>
      )}
      {chipMenu.menu && (
        <SuggestChipMenu
          menu={chipMenu.menu}
          pinned={isQuickReplyPinned(pinnedList, chipMenu.menu.text)}
          onClose={chipMenu.close}
          onTogglePin={onTogglePin}
          onForget={onForget}
        />
      )}
    </>
  );
}
