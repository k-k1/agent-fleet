import { useEffect } from "react";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { tMaybe, useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";
import { parseOAuthReturn, rememberReturn, stripOAuthReturn } from "./mcpOAuth.ts";

// McpOAuthReturn handles the browser coming back from an MCP server's authorization server
// (`/?mcp_oauth=ok|failed&server=&reason=&registration=`, written by the Control Plane's
// callback). Once the console has booted it says how it went, opens Settings on the MCP tab,
// and removes the parameters so a reload does not replay the notice. Nothing in the query is
// shown as received: the reason picks one of our own texts.
export function McpOAuthReturn({ booted }: { booted: boolean }) {
  const tr = useT();
  const toast = useToast();
  useEffect(() => {
    if (!booted) return;
    const r = parseOAuthReturn(window.location.search);
    if (!r) return;
    window.history.replaceState(
      window.history.state,
      "",
      window.location.pathname + stripOAuthReturn(window.location.search) + window.location.hash,
    );
    // The Control Plane always names the server on a sign-in that happened; a success without
    // one is a made-up URL, so it is neither announced nor acted on.
    if (r.ok && !r.server) return;
    rememberReturn(r);
    if (r.ok) {
      toast(tr("mcp.oauth_return_ok_generic"));
    } else {
      const why = tMaybe("mcp.oauth_return_" + r.reason) ?? tr("mcp.oauth_return_failed_generic");
      toast(tr("mcp.oauth_return_failed", { reason: why }));
    }
    useSettingsUI.getState().openSettings("mcp");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [booted]);
  return null;
}
