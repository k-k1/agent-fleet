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
      {/* 返信サジェスト: 常用短文＋直近回答に沿った候補（Layer A）＋✨の LLM 候補（v2）。
          クリックで差し込み・⌥で即送信。 */}
      {show && (
        <div className="chat-suggest" ref={attachSuggestRow}>
          {showAiButton && (
            <button
              type="button"
              className="chat-suggest-ai"
              title={tr("chat.suggest_ai")}
              disabled={suggesting}
              onClick={onFetchLlmSuggestions}
              onKeyDown={onSuggestNav} // Enter は既定の click（＝候補取得）に任せる
            >
              <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} />
            </button>
          )}
          {suggestChips.map((sg) => (
            // ピン留めは先頭固定＋📌。削除/ピンは右クリック・長タップ・Menu キーのメニューから。
            <button
              key={(sg.llm ? "l:" : "a:") + sg.text}
              type="button"
              className={
                "chat-suggest-chip" +
                (sg.llm ? " llm" : "") +
                (isQuickReplyPinned(pinnedList, sg.text) ? " pinned" : "") +
                (sg.text === cycledText ? " cycling" : "") // Tab でいま入力欄に入れている候補
              }
              aria-current={sg.text === cycledText ? "true" : undefined}
              title={tr("mirror.suggest_hint")}
              onClick={(e) => {
                if (chipMenu.clickSwallowed()) return; // 長タップでメニューを出した指離し
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
