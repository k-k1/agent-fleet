// TokenCapabilities says what an MCP token reaches: the session tools, the memory tools and how
// the token's scope maps to them. Static text only, so it renders the same while the workspace
// is stopped; the memory tools' own stopped-workspace answer is described, not fetched.
// The tool names and tiers mirror control-plane/internal/mcpsrv (mcp.go, mcp_memory.go).

import { useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";

export function TokenCapabilities() {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <div className="pat-can">
      <h4>{tr("tokens.can_head")}</h4>
      <ul className="muted ds-note">
        <li>{tr("tokens.can_sessions")}</li>
        <li>{tr("tokens.can_memory_read")}</li>
        <li>{tr("tokens.can_memory_write")}</li>
        <li>
          {tr("tokens.can_memory_setting_pre")}
          <button type="button" className="linklike" onClick={() => openSettings("memory:settings")}>
            {tr("tokens.can_memory_setting_link")}
          </button>
          {tr("tokens.can_memory_setting_post")}
        </li>
        <li>{tr("tokens.can_stopped")}</li>
      </ul>
    </div>
  );
}
