import { useState } from "react";
import { api, apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { useSettings, setSetting } from "../../../lib/settings.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { SettingRow, CardSettings, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// Claude: OAuth connect (start → approve in a new tab → paste code → complete), plus
// its behavior settings (Remote Control / notifications / RTK) once connected.
export function ClaudeCard({
  running,
  st,
  reload,
  claude,
  updateClaude,
}: {
  running: boolean;
  st: any;
  reload: () => void;
  claude: any;
  updateClaude: (patch: unknown) => void;
}) {
  const tr = useT();
  const s = useSettings();
  const toast = useToast();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);

  const start = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/claude/start", { method: "POST" });
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
      const r = await apiJSON("api/connections/claude/complete", "POST", { flow_id: flow.flow_id, code: c });
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
  // this signs out and reopens the same OAuth flow as one action. When the token is revoked
  // server side, `claude auth status` still reports loggedIn from the local credentials and the
  // card stays "connected" — without this control, an expired login can only be fixed by
  // guessing at disconnect.
  const reauth = async () => {
    await raw("api/connections/claude", { method: "DELETE" });
    reload(); // put the status pill back to "not connected"; the flow view below takes over first
    await start();
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
      ) : /* Check the flow before the connection state: re-auth runs sign-out then flow-start,
            and the api/connections refetch lands later. Checking "connected" first would hide
            the just-opened code field for that instant. */
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
              {/* With a plain <input>, password managers and browser autofill kick in and can
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
            <span className="p-exp" title={st.expires_at ? new Date(st.expires_at).toLocaleString() : undefined}>
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
          <ReauthButton onClick={() => void reauth()} />
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
      <CardSettings>
        <LaunchDefaults kind="claude" />
        <SettingRow label={tr("agents.claude_abort_resume")}>
          <OnOff
            value={s.claudeAbortAutoResume}
            onChange={(v) => setSetting("claudeAbortAutoResume", v)}
          />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_abort_resume")}</p>
        <SettingRow label={tr("agents.claude_rate_limit_resume")}>
          <OnOff value={s.rateLimitAutoResume} onChange={(v) => setSetting("rateLimitAutoResume", v)} />
        </SettingRow>
        <p className="ps-note">{tr("agents.note_claude_rate_limit_resume")}</p>
        {/* Remote Control / notifications / RTK are workspace-level files (independent of Claude
            auth) — pre-settable, but need the api/claude/settings endpoint loaded. */}
        {claude && (
          <>
            <SettingRow label={tr("agents.remote_control")}>
              <OnOff
                value={claude.remoteControlAtStartup}
                onChange={(v) => updateClaude({ remoteControlAtStartup: v })}
              />
            </SettingRow>
            <SettingRow label={tr("agents.notifications")}>
              <OnOff
                value={claude.agentPushNotifEnabled}
                onChange={(v) => updateClaude({ agentPushNotifEnabled: v })}
              />
            </SettingRow>
            <RtkRow
              available={claude.rtk_available}
              value={claude.rtk_enabled}
              onChange={(v) => updateClaude({ rtk: v })}
            />
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}
