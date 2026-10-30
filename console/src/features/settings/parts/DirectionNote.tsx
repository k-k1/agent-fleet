// DirectionNote — the one-line "which way does this tab point" statement at the top of the MCP
// servers tab (outbound) and the MCP tokens tab (inbound). The two sit in different rail groups
// but read as a pair by name; the link to the other side is what stops a member registering this
// Workspace's own /mcp as a server for sessions.
import { useT, type MsgKey } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";

export function DirectionNote({ text, linkLabel, target }: { text: MsgKey; linkLabel: MsgKey; target: string }) {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <p className="muted ds-note" data-testid="direction-note">
      {tr(text)}{" "}
      <button type="button" className="linklike" onClick={() => openSettings(target)}>
        {tr(linkLabel)}
      </button>
    </p>
  );
}
