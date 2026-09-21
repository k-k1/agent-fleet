import { useCallback, useEffect, useRef, useState } from "react";
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
//
// Auto-check on open (docs/log/107 follow-up): a card that just sits there saying "connected"
// looked identical whether the LAN box was answering or not — nobody presses "check" on open.
// So when the card mounts with a connection saved and `st.reachable` UNDEFINED (this process
// has never observed it — the Agent's own doc comment on lcppStatus explains why GET
// /connections cannot tell us that itself), fire the same check exactly once. Once `st`
// carries a real true/false, this effect does nothing — reopening the settings modal must not
// re-dial a LAN box that already answered (or already didn't) a moment ago.
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
  const reachable = st?.reachable;
  const autoChecked = useRef(false);

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
      autoChecked.current = false; // a freshly saved connection has never been observed — let the effect below check it
      reload();
    } finally {
      setBusy(false);
    }
  };

  const disconnect = async () => {
    await raw("api/connections/lcpp", { method: "DELETE" });
    setCheck(null);
    autoChecked.current = false;
    reload();
  };

  const runCheck = useCallback(async () => {
    setCheck({ state: "checking" });
    try {
      const res = await apiJSON("api/connections/lcpp/check", "POST", {});
      if (!res || res.error) {
        setCheck({ state: "error", msg: res?.error ? errDetail(res.error) : "" });
      } else {
        setCheck({ state: "ok", build: res.build_info || "", nctx: res.n_ctx || 0, models: res.models || [] });
      }
    } catch {
      setCheck({ state: "error", msg: "" });
    } finally {
      // The check itself just recorded an observation on the Agent (connections.go's
      // handleCheckLcppConn) — reload so `st.reachable` picks it up, which is also what stops
      // the auto-check effect below from firing again.
      reload();
    }
  }, [reload]);

  useEffect(() => {
    if (!running || !connected || reachable !== undefined || autoChecked.current) return;
    autoChecked.current = true;
    void runCheck();
  }, [running, connected, reachable, runCheck]);

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
              {/* The known reachability, independent of `check` (which is this MOUNT's own
                  transient checking/ok/error state, reset to null on every remount). Once
                  `st.reachable` is known it stays known across a settings-modal close/reopen,
                  so this line must not go blank just because `check` has not run again yet —
                  it is only hidden while `check` has something more detailed to say. */}
              {reachable !== undefined && !check && (
                <p className={"ps-note" + (reachable ? "" : " ps-note-warn")}>
                  {tr(reachable ? "agents.lcpp_conn_reachable" : "agents.lcpp_conn_unreachable")}
                  {/* The model the last real /v1/models read actually found (docs/log/107,
                      2026-09-21 addendum) — a member can swap the LAN box under the SAME URL,
                      and a single-model llama-server does not read the request's own `model`
                      field, so this is the only thing on screen that can catch a swap. */}
                  {reachable && st?.model && (
                    <>
                      {tr("ui.sep")}
                      {tr("agents.lcpp_conn_model") + st.model}
                      {st.model_count && st.model_count > 1 ? tr("agents.lcpp_conn_model_more", { n: st.model_count - 1 }) : null}
                    </>
                  )}
                </p>
              )}
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
