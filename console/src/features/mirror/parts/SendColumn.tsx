import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { SplitSend } from "../sendQueue/SplitSend.tsx";

/**
 * Right column: a small mode chip stacked over the send button. The chip is a
 * rarely-used control, so it rides above send (compact, not competing with the
 * textarea) and only appears for agents with a plan toggle.
 */
export function SendColumn({
  showMode,
  isPlan,
  modeLabel,
  modeDisabled,
  sendDisabled,
  onToggleMode,
  onSend,
  onQueue,
}: {
  showMode: boolean;
  isPlan: boolean;
  /** The mode name the terminal reported, or an ellipsis while none has arrived yet. */
  modeLabel: string;
  modeDisabled: boolean;
  sendDisabled: boolean;
  onToggleMode: () => void;
  onSend: () => void;
  /** Present while a turn runs: hold the draft in the pre-send queue instead of sending. */
  onQueue?: () => void;
}) {
  return (
    <div className="mirror-send-col">
      {showMode && (
        <button
          type="button"
          className={"mirror-mode" + (isPlan ? " on" : "")}
          disabled={modeDisabled}
          title={tr("mirror.toggle_mode")}
          onClick={onToggleMode}
        >
          {modeLabel || "…"}
        </button>
      )}
      {onQueue ? (
        <SplitSend disabled={sendDisabled} onSend={onSend} onQueue={onQueue} />
      ) : (
        <button type="button" className="btn primary mirror-send" disabled={sendDisabled} onClick={onSend} title={tr("chat.send")}>
          <Icon name="send" />
        </button>
      )}
    </div>
  );
}
