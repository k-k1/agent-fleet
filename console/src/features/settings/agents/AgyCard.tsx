import { useState } from "react";
import { apiJSON, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "../parts/providerCard.tsx";
import { CardSettings, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

export function AgyCard({
  running,
  st,
  reload,
  agents,
  updateAgents,
}: {
  running: boolean;
  st: any;
  reload: () => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id }
  const [code, setCode] = useState("");
  const [busy, setBusy] = useState(false);
  // Fixed to "oauth" while M1; the selector ships disabled so the M2 wiring
  // (method: "gcp-project" + project_id) has its place already cut.
  const method = "oauth";

  const start = async () => {
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/agy/start", "POST", { method });
      if (!res || res.error || !res.url) {
        toast(tr("agents.agy_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      window.open(res.url, "_blank", "noopener");
      setFlow({ url: res.url, flow_id: res.flow_id });
    } finally {
      setBusy(false);
    }
  };
  const complete = async () => {
    // Same autofill guard as ClaudeCard: cut anything from http(s):// on, in case
    // a password manager appended a URL to the pasted authorization code.
    let c = code.trim();
    const u = c.search(/https?:\/\//i);
    if (u > 0) c = c.slice(0, u).trim();
    if (!c) return;
    setBusy(true);
    try {
      const r = await apiJSON("api/connections/agy/complete", "POST", { flow_id: flow.flow_id, code: c });
      if (r && r.error) {
        toast(tr("conn.connect_failed", { msg: String(r.error.message || r.error) }));
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
    await raw("api/connections/agy", { method: "DELETE" });
    reload();
  };

  const unsupported = st?.supported === false;
  return (
    <ProviderCard
      id="agy"
      name={kindDisplayName("agy")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {/* 実験枠 label — always visible, connected or not (採用条件). */}
      <p className="ps-note ps-note-warn agy-exp">{tr("agents.agy_exp_label")}</p>
      {!running ? (
        <ConnPaused />
      ) : unsupported ? (
        <div className="p-desc">{tr("agents.agy_unsupported", { reason: st?.reason || "" })}</div>
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || tr("conn.connected")}>
            {st.email || tr("conn.connected")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : flow ? (
        <>
          <div className="p-desc">{tr("agents.agy_desc_flow")}</div>
          <div className="p-body">
            <Hint>
              {tr("agents.claude_hint_1")}
              <a href={flow.url} target="_blank" rel="noopener" className="flow-link">
                {tr("agents.claude_signin_link")}
              </a>
              {tr("agents.claude_hint_2")}
            </Hint>
            <div className="flow">
              <input
                className="cinput"
                type="text"
                name="agy-oauth-code"
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
      ) : (
        <>
          <div className="p-desc">{tr("agents.agy_desc")}</div>
          <div className="p-body">
            <div className="flow">
              <select className="cinput" value={method} disabled title={tr("agents.agy_method_label")}>
                <option value="oauth">{tr("agents.agy_method_oauth")}</option>
                <option value="gcp-project" disabled>
                  {tr("agents.agy_method_gcp")}
                </option>
              </select>
              <button disabled={busy} onClick={start}>
                {tr("agents.oauth_connect")}
              </button>
            </div>
          </div>
        </>
      )}
      {/* RTK is a workspace-level flag (independent of agy auth) — pre-settable,
          same block shape as the Codex / opencode cards. */}
      <CardSettings>
        <LaunchDefaults kind="agy" />
        {agents && agents !== false && (
          <>
            <RtkRow
              available={agents.rtk_available}
              value={agents.agy_rtk}
              onChange={(v) => updateAgents({ agy_rtk: v })}
            />
            <p className="ps-note">{tr("agents.agy_rtk_note")}</p>
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}
