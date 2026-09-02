import type { KeyboardEvent as RKeyboardEvent, MouseEvent as RMouseEvent, Ref } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { isQuickReplyPinned } from "../../../lib/quickReplies.ts";
import type { ChipMenuHandlers } from "../SuggestChipMenu.tsx";

export type SuggestChip = { text: string; llm: boolean };

/**
 * 返信サジェスト: 常用短文＋直近回答に沿った候補（Layer A）＋✨で取得する LLM 候補（v2）。
 * クリックで差し込み、⌥で即送信。flex 全幅 (.mirror-suggest) で入力行の上に載る。
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
  /** 設定に保存されたピン留め済みの文（isQuickReplyPinned に渡す生の配列）。 */
  pinned: string[] | undefined;
  /** Tab 補完でいま入力欄に入っている候補（強調用）。null = サイクル中でない。 */
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
          disabled={suggesting || !running} // wsDown() はトースト副作用があるのでレンダー中は呼ばない
          onClick={onFetchLlm}
          onKeyDown={onNav} // Enter は既定の click（＝候補取得）に任せる
        >
          <Icon name={suggesting ? "loading" : "sparkle"} spin={suggesting} />
        </button>
      )}
      {chips.map((sg) => (
        // ピン留めした候補は先頭に固定で並び、📌 を付けて「消えない側」だと分かるようにする。
        // 削除・ピン留めは右クリック / 長タップ / Menu キーのメニュー（SuggestChipMenu）。
        <button
          key={(sg.llm ? "l:" : "a:") + sg.text}
          type="button"
          className={
            "mirror-suggest-chip" +
            (sg.llm ? " llm" : "") +
            (isQuickReplyPinned(pinned, sg.text) ? " pinned" : "") +
            (sg.text === cycledText ? " cycling" : "") // Tab でいま入力欄に入れている候補
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
