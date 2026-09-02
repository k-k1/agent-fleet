import { useState } from "react";
import { api, apiJSON, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { OnOff } from "../parts/controls.tsx";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, IssueLink } from "../parts/providerCard.tsx";
import { usePolling } from "../parts/usePolling.ts";
import { SettingRow, CardSettings, ThinkingRow, ConnPaused, LaunchDefaults, RtkRow } from "./AgentCardParts.tsx";

// Codex: ChatGPT subscription (device code) or API key, plus the RTK toggle
// (workspace-level; shown whenever settings load). codex has no command-rewrite
// hook so RTK there is instruction-based.
export function CodexCard({
  running,
  st,
  reload,
  codex,
  updateCodex,
  agents,
  updateAgents,
}: {
  running: boolean;
  st: any;
  reload: () => void;
  codex: any;
  updateCodex: (patch: unknown) => void;
  agents: any;
  updateAgents: (patch: unknown) => void;
}) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | device | key
  const [dev, setDev] = useState<any>(null); // { user_code, url, flow_id, status }
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);

  const startDevice = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/codex/device/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.codex_auth_failed", { msg: res?.error?.message || tr("agents.codex_device_disabled") }));
        return;
      }
      setMode("device");
      setDev({ user_code: res.user_code, url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setDev((d: any) => ({ ...d, status: tr("git.oauth_expired") })),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/codex/device/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setMode("idle");
            reload();
            return { stop: true };
          }
          return { stop: false, nextMs: 2500 };
        },
      });
    } finally {
      setBusy(false);
    }
  };

  const saveKey = async () => {
    if (!key.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/codex/api-key", "POST", { key: key.trim() });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: String(res.error.message || res.error) }));
        return;
      }
      setKey("");
      setMode("idle");
      reload();
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/codex", { method: "DELETE" });
    reload();
  };

  return (
    <ProviderCard
      id="codex"
      name={kindDisplayName("codex")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || (st.method === "apikey" ? tr("agents.codex_apikey_label") : "ChatGPT")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : mode === "device" && dev ? (
        <div className="p-body">
          <DeviceSteps code={dev.user_code} url={dev.url} status={dev.status} />
          <Hint>
            {tr("agents.codex_hint1_1")}
            <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
              {tr("agents.codex_settings_security")}
            </a>
            {tr("agents.codex_hint1_2")}
          </Hint>
        </div>
      ) : mode === "key" ? (
        <div className="p-body">
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("agents.openai_key_placeholder")}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              autoFocus
            />
            <button disabled={busy || !key.trim()} onClick={saveKey}>
              {tr("conn.connect")}
            </button>
            <button className="ghost" onClick={() => setMode("idle")}>
              {tr("common.back")}
            </button>
          </div>
          <IssueLink url="https://platform.openai.com/api-keys" />
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.codex_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startDevice}>
                <span className="p-opt-t">
                  {tr("agents.codex_connect_sub")} <span className="p-rec">{tr("git.recommended")}</span>
                </span>
                <span className="p-opt-s">{tr("agents.codex_sub_note")}</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("key")}>
                <span className="p-opt-t">{tr("agents.codex_connect_key")}</span>
                <span className="p-opt-s">{tr("agents.codex_key_note")}</span>
              </button>
            </div>
            <Hint>
              {tr("agents.codex_hint2_1")}
              <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
                {tr("agents.codex_settings_security")}
              </a>
              {tr("agents.codex_hint2_2")}
            </Hint>
          </div>
        </>
      )}
      {/* RTK is a workspace-level flag (independent of Codex auth) — pre-settable. */}
      <CardSettings>
        <LaunchDefaults kind="codex" />
        <ThinkingRow kind="codex" />
        {codex && (
          <>
            <SettingRow label={tr("agents.codex_nudge")}>
              <OnOff
                value={codex.rate_limit_model_nudge}
                onChange={(v) => updateCodex({ rate_limit_model_nudge: v })}
              />
            </SettingRow>
            <p className={`ps-note${codex.rate_limit_model_nudge ? " ps-note-warn" : ""}`}>
              {codex.rate_limit_model_nudge ? tr("agents.codex_nudge_on") : tr("agents.codex_nudge_off")}
            </p>
          </>
        )}
        {agents && agents !== false && (
          <>
            <RtkRow
              available={agents.rtk_available}
              value={agents.codex_rtk}
              onChange={(v) => updateAgents({ codex_rtk: v })}
            />
            <p className="ps-note">{tr("agents.codex_rtk_note")}</p>
          </>
        )}
      </CardSettings>
    </ProviderCard>
  );
}
