import { useState } from "react";
import { errDetail, raw } from "../../../core/api/client.ts";
import { startClaudeLogin, completeClaudeLogin } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { DATETIME_LOCALE_DEFAULT, fmtDateTime } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { ConnPaused, type LoginFlow } from "../agents/AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// Claude: OAuth connect (start → approve in a new tab → paste code → complete). Its behaviour
// settings (Remote Control / notifications / idle compaction / RTK) are on the Agents page.
export function ClaudeCard({
  running,
  st,
  reload,
}: {
  running: boolean;
  st: ProviderConn | undefined;
  reload: () => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [flow, setFlow] = useState<LoginFlow | null>(null);
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setBusy(true);
    try {
      const res = await startClaudeLogin();
      if (!res || res.error || !res.url) {
        toast(tr("agents.claude_auth_failed", { msg: res?.error ? errDetail(res.error) : "" }));
        return;
      }
      window.open(res.url, "_blank", "noopener");
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    // The OAuth code has the form code#state. Autofill and the like can append a URL to it, so
    // cut everything from http(s):// onwards before sending.
    let c = code.trim();
    const u = c.search(/https?:\/\//i);
    if (u > 0) c = c.slice(0, u).trim();
    if (!c) return;
    setBusy(true);
    try {
      const r = await completeClaudeLogin(flow!.flow_id, c);
      if (r && r.error) {
        toast(tr("conn.connect_failed", { msg: errDetail(r.error) }));
        return;
      }
      setFlow(null);
      setCode("");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/claude", { method: "DELETE" });
    reload();
  };
  // Re-authenticate. claude owns its own .credentials.json and has no refresh-only command, so
  // this reopens the same OAuth flow. When the token is revoked server side, `claude auth status`
  // still reports loggedIn from the local credentials and the card stays "connected" — without
  // this control, an expired login can only be fixed by guessing at disconnect.
  //
  // It does not sign out first: `claude auth login` leaves the current credentials in place
  // until the pasted code is accepted, and the Agent waits for the CLI's own success rather
  // than for status, so backing out of the flow keeps the login sessions are using.
  const reauth = () => start();
  const back = () => {
    setFlow(null);
    setCode("");
  };

  return (
    <ProviderCard
      id="claude"
      name={kindDisplayName("claude")}
      status={
        running ? (
          /* Expired is not "connected": the credentials are local, so `claude auth status`
             returns loggedIn, but no turn will start (docs/log/47 §4-8). Leaving the pill
             green makes this screen the exact place that lies. */
          <StatusPill on={st?.connected && !st?.expired}>
            {!st?.connected
              ? tr("conn.disconnected")
              : st?.expired
                ? tr("conn.expired")
                : tr("conn.connected")}
          </StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : /* Check the flow before the connection state: re-auth opens the flow while the card is
            still connected, and checking "connected" first would hide the code field. */
      flow ? (
        <>
          <div className="p-desc">{tr("agents.claude_desc_flow")}</div>
          <div className="p-body">
            <Hint>
              {tr("agents.claude_hint_1")}
              <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
                {tr("agents.claude_signin_link")}
              </a>
              {tr("agents.claude_hint_2")}
            </Hint>
            <div className="flow">
              {/* With a plain input element, password managers and browser autofill kick in and can
                  append a claude.com URL to the pasted OAuth code (code#state), breaking it.
                  Disable autofill completely. */}
              <input
                className="cinput"
                type="text"
                name="claude-oauth-code"
                placeholder={tr("agents.paste_code")}
                value={code}
                onChange={(e) => setCode(e.target.value)}
                autoComplete="off"
                autoCorrect="off"
                autoCapitalize="off"
                spellCheck={false}
                data-1p-ignore
                data-lpignore="true"
                data-form-type="other"
                autoFocus
              />
              <button disabled={busy} onClick={complete}>
                {tr("agents.complete")}
              </button>
              <button className="ghost" disabled={busy} onClick={back}>
                {tr("common.back")}
              </button>
            </div>
          </div>
        </>
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || tr("conn.connected")}>
            {st.email || tr("conn.connected")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          {/* Expiry (docs/log/47 §4-8). The CLI only warns via a startup hint that appears with
              under a day left and vanishes after 15 seconds, and says nothing once expired.
              This surface does not disappear, so it shows the expiry quietly beforehand; the
              exact timestamp is a tooltip, to keep the row from growing. */}
          {(st.expired || st.days_left !== undefined) && (
            <span className="p-exp" title={st.expires_at ? fmtDateTime(st.expires_at, DATETIME_LOCALE_DEFAULT) : undefined}>
              {st.expired
                ? tr("conn.expired")
                : st.days_left
                  ? tr("conn.expires_in", { days: st.days_left })
                  : /* Under a day left, or the refresh deadline has passed but the last access
                       token still works (it will stop within hours). Neither can be stated in
                       whole days. */
                    tr("conn.expires_soon")}
            </span>
          )}
          <ReauthButton disabled={busy} onClick={() => void reauth()} />
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.claude_desc")}</div>
          <div className="p-body">
            <button disabled={busy} onClick={start}>
              {tr("agents.oauth_connect")}
            </button>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
