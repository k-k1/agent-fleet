import type { KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent, Ref } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { isQuickReplyPinned } from "../../../lib/quickReplies.ts";
import type { ChipMenuHandlers } from "../SuggestChipMenu.tsx";

export type SuggestChip = { text: string; llm: boolean };

/**
 * Reply suggestions: frequently used short replies plus candidates derived from the latest answer
 * (layer A), plus LLM candidates fetched on demand from the sparkle button. A click inserts the
 * text; Option-click sends it immediately. Full-width flex row (.mirror-suggest) above the input.
 */
export function SuggestRow({
  rowRef,
  chips,
  pinned,
  cycledText,
  aiEnabled,
  suggesting,
  running,
  onFetchLlm,
  onNav,
  onChipKeyDown,
  onChipClick,
  chipProps,
}: {
  rowRef: Ref<HTMLDivElement>;
  chips: SuggestChip[];
  /** Pinned replies as stored in settings (the raw array passed to isQuickReplyPinned). */
  pinned: string[] | undefined;
  /** The candidate Tab completion has currently filled into the input, for highlighting.
   *  null means no cycling is in progress. */
  cycledText: string | null;
  aiEnabled: boolean;
  suggesting: boolean;
  running: boolean;
  onFetchLlm: () => void;
  onNav: (e: RKeyboardEvent<HTMLButtonElement>) => boolean;
  onChipKeyDown: (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => void;
  onChipClick: (e: RMouseEvent<HTMLButtonElement>, text: string) => void;
  chipProps: (text: string, llm: boolean) => ChipMenuHandlers;
}) {
  return (
    <div className="mirror-suggest" ref={rowRef}>
      {aiEnabled && (
        <button
          type="button"
          className="mirror-suggest-ai"
          title={tr("mirror.suggest_ai")}
          disabled={suggesting || !running} // wsDown() has a toast side effect; never call it during render
          onClick={onFetchLlm}
          onKeyDown={onNav} // leave Enter to the default click (which fetches the candidates)
        >
          <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} />
        </button>
      )}
      {chips.map((sg) => (
        // Pinned suggestions are fixed at the head of the row and carry a pin glyph so it is
        // clear they will not disappear. Delete and pin live in the right-click / long-press /
        // Menu-key menu (SuggestChipMenu).
        <button
          key={(sg.llm ? "l:" : "a:") + sg.text}
          type="button"
          className={
            "mirror-suggest-chip" +
            (sg.llm ? " llm" : "") +
            (isQuickReplyPinned(pinned, sg.text) ? " pinned" : "") +
            (sg.text === cycledText ? " cycling" : "") // the candidate Tab currently has in the input
          }
          aria-current={sg.text === cycledText ? "true" : undefined}
          title={tr("mirror.suggest_hint")}
          onClick={(e) => onChipClick(e, sg.text)}
          onKeyDown={(e) => onChipKeyDown(e, sg.text, sg.llm)}
          {...chipProps(sg.text, sg.llm)}
        >
          {isQuickReplyPinned(pinned, sg.text) && <Icon name="pinned" className="mirror-suggest-pin" />}
          {sg.text}
        </button>
      ))}
    </div>
  );
}
