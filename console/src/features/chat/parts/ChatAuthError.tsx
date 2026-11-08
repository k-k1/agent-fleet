import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

// ChatAuthError replaces the bare turn error when Claude reported that its login expired
// (../claudeAuthError.ts). The raw CLI text stays one click away for diagnosis; the message says
// what to do, the button leads to the sign-in card, and Retry re-sends the turn that failed.
export function ChatAuthError({
  text,
  onReauth,
  onRetry,
}: {
  text: string;
  onReauth: () => void;
  onRetry?: () => void;
}) {
  const tr = useT();
  return (
    <div className="chat-error chat-auth-error" role="alert">
      <p className="cae-msg">{tr("chat.auth_expired")}</p>
      <div className="cae-actions">
        <button type="button" className="mef-action" onClick={onReauth}>
          <Icon name="plug" />
          {tr("chat.auth_expired_action")}
        </button>
        {onRetry && (
          <button type="button" className="ghost" onClick={onRetry}>
            <Icon name="refresh" />
            {tr("chat.auth_expired_retry")}
          </button>
        )}
      </div>
      <details className="cae-details">
        <summary>{tr("chat.auth_expired_details")}</summary>
        <pre className="cae-raw">{text}</pre>
      </details>
    </div>
  );
}
