import { useCallback, useEffect, useRef, useState } from "react";
import { apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DisconnectButton } from "../parts/providerCard.tsx";
import { ConnPaused } from "../agents/AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// LcppCard (docs/log/105 §106.2 / docs/log/107): the member's OWN connection to a LAN
// llama-server, on Model credentials under Self-hosted. Whether llama.cpp may be launched at all
// is a harness switch and stays on the Agents page (LcppBehavior). Unlike every other
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
export function LcppCard({
  running,
  st,
  reload,
  endpointAllowed,
}: {
  running: boolean;
  st: ProviderConn | undefined;
  reload: () => void;
  /** Whether the policy allows the endpoint stored now (ADR 0138 §2). One it does not is kept but
   *  not shown (D14a): the card offers the entry, which replaces it on an approved host. */
  endpointAllowed: boolean;
}) {
  const tr = useT();
  const toast = useToast();
  const [url, setUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [check, setCheck] = useState<
    null | { state: "checking" } | { state: "ok"; build: string; nctx: number; models: string[] } | { state: "error"; msg: string }
  >(null);
  const connected = !!st?.connected && endpointAllowed;
  const reachable = connected ? st?.reachable : undefined;
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
        running ? (
          <StatusPill on={connected}>{connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running && <ConnPaused />}
      {running && (
        <div className="p-body">
          <p className="ps-title">{tr("agents.lcpp_conn_title")}</p>
          {connected ? (
            <>
              <div className="p-who p-who-flush">
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
            </>
          ) : (
            <>
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
            </>
          )}
        </div>
      )}

    </ProviderCard>
  );
}
