import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";

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
}: {
  showMode: boolean;
  isPlan: boolean;
  /** 端末が名乗ったモード名。まだ来ていなければ「…」。 */
  modeLabel: string;
  modeDisabled: boolean;
  sendDisabled: boolean;
  onToggleMode: () => void;
  onSend: () => void;
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
      <button type="button" className="btn primary mirror-send" disabled={sendDisabled} onClick={onSend} title={tr("chat.send")}>
        <Icon name="send" />
      </button>
    </div>
  );
}
