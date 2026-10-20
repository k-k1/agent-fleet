// ExternalMemoryHelpLink points the externalMemory control at the MCP tokens tab, where the
// token, the client config and the tool list live. It is one standalone component so it can be
// mounted wherever the control ends up; it carries no steps of its own and reads no Agent data.

import { useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";

export function ExternalMemoryHelpLink() {
  const tr = useT();
  const openSettings = useSettingsUI((s) => s.openSettings);
  return (
    <p className="muted ds-note">
      <button type="button" className="linklike" onClick={() => openSettings("tokens")}>
        {tr("mem.ext_help")}
      </button>
    </p>
  );
}
