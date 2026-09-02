import type { KeyboardEvent, ClipboardEvent, RefObject } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// ChatComposerRow is the input line itself: the phone-only ↑/↓ history nav, the textarea
// and the send / stop button. Every decision it draws (can we send, is a turn running,
// which placeholder) is resolved by ChatView and handed down.
export function ChatComposerRow({
  input,
  inputRef,
  disabled,
  canSend,
  canAttach,
  modSend,
  hasTarget,
  history,
  histIdx,
  onRecallPrev,
  onRecallNext,
  onInput,
  onKeyDown,
  onPaste,
  streaming,
  onStop,
  onSend,
}: {
  input: string;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** 入力欄・履歴ボタンを塞ぐか（会話が無い or ターン進行中）。 */
  disabled: boolean;
  /** 送信ボタンを押せるか（本文か添付があるか）。 */
  canSend: boolean;
  canAttach: boolean;
  modSend: boolean;
  /** 会話 or 下書きが定まっているか（プレースホルダの分岐）。 */
  hasTarget: boolean;
  history: string[];
  histIdx: number | null;
  onRecallPrev: () => void;
  onRecallNext: () => void;
  onInput: (value: string) => void;
  onKeyDown: (e: KeyboardEvent<HTMLTextAreaElement>) => void;
  onPaste: (e: ClipboardEvent<HTMLTextAreaElement>) => void;
  /** 中断ボタンを出すか（送信中、または離脱したターンへの再接続中）。 */
  streaming: boolean;
  onStop: () => void;
  onSend: () => void;
}) {
  const tr = useT();
  return (
    <div className="chat-composer-row">
      {/* History nav for phones (no arrow keys); hidden on wider screens via CSS. */}
      <div className="chat-hist">
        <button
          type="button"
          className="btn chat-hist-btn"
          title={tr("chat.prev_input")}
          disabled={!history.length || disabled}
          onClick={onRecallPrev}
        >
          <Icon name="chevron-up" />
        </button>
        <button
          type="button"
          className="btn chat-hist-btn"
          title={tr("chat.next_input")}
          disabled={histIdx === null || disabled}
          onClick={onRecallNext}
        >
          <Icon name="chevron-down" />
        </button>
      </div>
      <textarea
        ref={inputRef}
        className="chat-input"
        value={input}
        placeholder={
          hasTarget
            ? canAttach
              ? modSend
                ? tr("chat.ph_mod_img")
                : tr("chat.ph_enter_img")
              : modSend
                ? tr("chat.ph_mod")
                : tr("chat.ph_enter")
            : tr("chat.ph_loading")
        }
        disabled={disabled}
        onChange={(e) => onInput(e.target.value)}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        rows={2}
      />
      {streaming ? (
        // reattaching: reloaded into a detached turn — stop still works via chatStop.
        <button type="button" className="btn chat-send chat-stop" onClick={onStop} title={tr("chat.stop")}>
          <Icon name="debug-stop" />
        </button>
      ) : (
        <button
          type="button"
          className="btn primary chat-send"
          disabled={disabled || !canSend}
          onClick={onSend}
          title={tr("chat.send")}
        >
          <Icon name="send" />
        </button>
      )}
    </div>
  );
}
