import { useEffect, useRef, useState } from "react";
import { Button } from "../../../ui/Button.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettingsUI } from "../store.ts";
import { parseOAuthReturn, rememberReturn, stripOAuthReturn } from "./mcpOAuth.ts";
import { announceSignInDone, outcomeText, takeSameTab } from "./mcpOAuthSignal.ts";
import type { SignInDone } from "./mcpOAuthSignal.ts";

// McpOAuthReturn handles the browser coming back from an MCP server's authorization server
// (`/?mcp_oauth=ok|failed&server=&reason=&registration=`, written by the Control Plane's
// callback). The sign-in normally runs in a tab of its own, so this is that tab: it says how it
// went, tells the tab the member started from (by server id and outcome code only), and says it
// can be closed. It never navigates anywhere on the strength of the query. When the browser
// blocked the new tab the sign-in ran in the Console tab itself (the same-tab marker), and
// the old behaviour applies: a notice, and Settings on the MCP tab. Either way the parameters are
// removed so a reload does not replay the notice. Nothing in the query is shown as received:
// the reason picks one of our own texts.
// How long to wait after window.close() before concluding the browser refused: a tab it will
// close is gone well within this, and one it will not (not opened by script) stays put.
const CLOSE_GRACE_MS = 300;

export function McpOAuthReturn({ booted }: { booted: boolean }) {
  const tr = useT();
  const [closeFailed, setCloseFailed] = useState(false);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(closeTimer.current), []);
  const toast = useToast();
  const [shown, setShown] = useState<SignInDone["outcome"] | null>(null);
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
    const outcome = r.ok ? "ok" : (r.reason ?? "failed");
    if (takeSameTab()) {
      toast(outcomeText(tr, outcome));
      useSettingsUI.getState().openSettings("mcp");
      return;
    }
    if (r.server) announceSignInDone({ server: r.server, outcome });
    setShown(outcome);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [booted]);
  // Only ever called from the button: closing on mount would hide the result before it is read.
  const closeTab = () => {
    window.close();
    clearTimeout(closeTimer.current);
    closeTimer.current = setTimeout(() => {
      if (!window.closed) setCloseFailed(true);
    }, CLOSE_GRACE_MS);
  };
  if (!shown) return null;
  return (
    <div className="mcp-oauth-return" role="dialog" aria-modal="true" data-testid="mcp-oauth-return">
      <div className="mcp-oauth-return-card">
        <strong>{outcomeText(tr, shown)}</strong>
        <p className="muted">{tr("mcp.oauth_return_tab_close")}</p>
        {closeFailed && (
          <p className="muted" role="status" data-testid="mcp-oauth-return-close-failed">
            {tr("mcp.oauth_return_close_failed")}
          </p>
        )}
        <div className="mcp-actions">
          <Button variant="primary" onClick={closeTab}>
            {tr("mcp.oauth_return_close")}
          </Button>
          <Button
            variant="ghost"
            onClick={() => {
              setShown(null);
              useSettingsUI.getState().openSettings("mcp");
            }}
          >
            {tr("mcp.oauth_return_stay")}
          </Button>
        </div>
      </div>
    </div>
  );
}
