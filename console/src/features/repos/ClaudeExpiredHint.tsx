// The expired-Claude-login note shared by the launch dialogs (StartModal, LaunchModal). Claude
// stays launchable with an expired login (/login in the pane recovers), so this is a notice, not
// a gate. It renders only for kind claude and only when the workspace reports the login expired.
import { openAgentsSettings } from "../../lib/authExpiredToast.tsx";
import { useT } from "../../lib/i18n/index.ts";

export function ClaudeExpiredHint({ kind, expired }: { kind: string; expired: boolean }) {
  const tr = useT();
  if (kind !== "claude" || !expired) return null;
  return (
    <span className="ui-field-hint">
      {tr("start.claude_expired")}
      <button type="button" className="linklike" onClick={openAgentsSettings}>
        {tr("mirror.error_auth_action")}
      </button>
    </span>
  );
}
