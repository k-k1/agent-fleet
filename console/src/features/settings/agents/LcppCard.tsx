import { useState } from "react";
import { apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "../parts/providerCard.tsx";
import { Choice } from "../parts/controls.tsx";
import { CardSettings, LaunchDefaults, SettingRow } from "./AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// LcppCard (docs/log/105 §106.2 / docs/log/107). llama.cpp has no sign-in of its own (ADR
// 0093 決定 10), so the switch above CardSettings is still the workspace-policy on/off — same
// reasoning as OpencodeUsageRows' "usage" switch. docs/log/107 adds a second, independent
// thing to this card: the member's OWN connection to a LAN llama-server. Unlike every other
// connection card, saving it does NOT authenticate against anything (connections.go's
// handlePutLcppConn validates only the URL's shape) — whether it actually answers is the
// separate "check connection" action below, which is why this card has three states instead
// of the usual two: no connection / connection saved / connection saved and just checked.
export function LcppCard({ running, st, reload }: { running: boolean; st: ProviderConn | undefined; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const s = useSettings();
  const enabled = s.lcppEnabled;
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [check, setCheck] = useState<
    null | { state: "checking" } | { state: "ok"; build: string; nctx: number; models: string[] } | { state: "error"; msg: string }
  >(null);
  const connected = !!st?.connected;

  const save = async () => {
    if (!url.trim()) return;
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/lcpp", "PUT", { url: url.trim(), apiKey: apiKey.trim() });
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errDetail(res.error) }));
        return;
      }
      setUrl("");
      setApiKey("");
      setCheck(null);
      reload();
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    await raw("api/connections/lcpp", { method: "DELETE" });
    setCheck(null);
    reload();
  };

  const runCheck = async () => {
    setCheck({ state: "checking" });
    try {
      const res = await apiJSON("api/connections/lcpp/check", "POST", {});
      if (!res || res.error) {
        setCheck({ state: "error", msg: res?.error ? errDetail(res.error) : "" });
        return;
      }
      setCheck({ state: "ok", build: res.build_info || "", nctx: res.n_ctx || 0, models: res.models || [] });
    } catch {
      setCheck({ state: "error", msg: "" });
    }
  };

  return (
    <ProviderCard
      id="lcpp"
      name={kindDisplayName("lcpp")}
      status={
        <StatusPill on={enabled}>
          {enabled ? tr("agents.lcpp_enabled_on") : tr("agents.lcpp_enabled_off")}
        </StatusPill>
      }
    >
      <SettingRow label={tr("agents.lcpp_enabled")}>
        <Choice
          value={enabled ? "on" : "off"}
          options={[
            ["off", tr("agents.lcpp_enabled_off")],
            ["on", tr("agents.lcpp_enabled_on")],
          ]}
          onChange={(v) => setSetting("lcppEnabled", v === "on")}
        />
      </SettingRow>
      <p className="ps-note">{tr(enabled ? "agents.lcpp_enabled_note_on" : "agents.lcpp_enabled_note_off")}</p>

      {running && (
        <>
          <p className="p-desc">{tr("agents.lcpp_conn_title")}</p>
          {connected ? (
            <>
              <div className="p-who">
                <span className="p-em">{st?.url}</span>
                <DisconnectButton onClick={disconnect} />
              </div>
              <div className="p-body">
                <div className="p-opts">
                  <button type="button" className="p-opt" disabled={check?.state === "checking"} onClick={runCheck}>
                    <span className="p-opt-t">
                      {check?.state === "checking" ? tr("agents.lcpp_conn_checking") : tr("agents.lcpp_conn_check")}
                    </span>
                  </button>
                </div>
                {check?.state === "ok" && (
                  <p className="ps-note">
                    {tr("agents.lcpp_conn_check_result", {
                      build: check.build || "?",
                      nctx: String(check.nctx || "?"),
                      models: check.models.length ? check.models.join(", ") : "?",
                    })}
                  </p>
                )}
                {check?.state === "error" && (
                  <p className="ps-note ps-note-warn">{tr("agents.lcpp_conn_check_failed", { msg: check.msg })}</p>
                )}
              </div>
            </>
          ) : (
            <div className="p-body">
              <div className="flow">
                <input
                  className="cinput"
                  type="text"
                  placeholder={tr("agents.lcpp_conn_url_placeholder")}
                  value={url}
                  onChange={(e) => setUrl(e.target.value)}
                />
              </div>
              <div className="flow">
                <input
                  className="cinput"
                  type="password"
                  placeholder={tr("agents.lcpp_conn_key_placeholder")}
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                />
                <button disabled={busy || !url.trim()} onClick={save}>
                  {tr("conn.connect")}
                </button>
              </div>
              <Hint>{tr("agents.lcpp_conn_note")}</Hint>
            </div>
          )}
        </>
      )}

      <CardSettings>
        <LaunchDefaults kind="lcpp" />
      </CardSettings>
    </ProviderCard>
  );
}
