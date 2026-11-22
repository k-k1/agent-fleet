import { useRef, useState } from "react";
import { errDetail, raw } from "../../../core/api/client.ts";
import { startCodexDeviceLogin, pollCodexDeviceLogin, putCodexApiKey } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import {
  ProviderCard,
  StatusPill,
  Hint,
  DeviceSteps,
  DisconnectButton,
  ReauthButton,
  IssueLink,
} from "../parts/providerCard.tsx";
import { usePolling } from "../parts/usePolling.ts";
import { ConnPaused, type DeviceFlow } from "../agents/AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// Codex: ChatGPT subscription (device code) or API key. Its RTK toggle and nudge are on the
// Agents page (AgentBehaviors).
export function CodexCard({
  running,
  st,
  reload,
  allowed,
}: {
  running: boolean;
  st: ProviderConn | undefined;
  reload: () => void;
  /** The routes the policy allows codex (ADR 0138 D10a). */
  allowed: Set<string>;
}) {
  const tr = useT();
  // Each control is judged by the route it stores a credential for: the ChatGPT sign-in by the
  // subscription, the API key by openai-api. A stored login on a forbidden route is not shown
  // (D14a); an allowed API key stays offered when the subscription is forbidden.
  const subAllowed = allowed.has("chatgpt-subscription");
  const keyAllowed = allowed.has("openai-api");
  const storedAllowed = st?.method === "apikey" ? keyAllowed : subAllowed;
  const connected = !!st?.connected && storedAllowed;
  const toast = useToast();
  const poll = usePolling();
  const [mode, setMode] = useState("idle"); // idle | device | key
  const [dev, setDev] = useState<DeviceFlow | null>(null);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  // The device flow the card is showing. A poll whose flow is no longer this one stops: after
  // Back, a code approved later must not replace the login the user chose to keep.
  const activeFlow = useRef<string | null>(null);

  const startDevice = async () => {
    setBusy(true);
    try {
      const res = await startCodexDeviceLogin();
      if (!res || res.error || !res.url) {
        toast(tr("agents.codex_auth_failed", { msg: res?.error ? errDetail(res.error) : tr("agents.codex_device_disabled") }));
        return;
      }
      activeFlow.current = res.flow_id;
      setMode("device");
      setDev({ user_code: res.user_code, url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => {
          if (activeFlow.current === res.flow_id) setDev((d) => (d ? { ...d, status: tr("git.oauth_expired"), ended: true } : d));
        },
        step: async () => {
          if (activeFlow.current !== res.flow_id) return { stop: true };
          let p;
          try {
            p = await pollCodexDeviceLogin(res.flow_id);
          } catch {
            p = null;
          }
          if (activeFlow.current !== res.flow_id) return { stop: true };
          if (p && p.connected) {
            activeFlow.current = null;
            setMode("idle");
            reload();
            return { stop: true };
          }
          // codex exited without signing in (refused, expired, broken), or the Agent took the
          // flow and could not install it (an error answer): nothing more will come.
          if (p && (p.failed || p.error)) {
            activeFlow.current = null;
            const msg = p.failed ? p.detail || "" : errDetail(p.error);
            const status = msg ? tr("provider.login_ended_detail", { msg }) : tr("provider.login_ended");
            setDev((d) => (d ? { ...d, status, ended: true } : d));
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
      const res = await putCodexApiKey(key.trim());
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errDetail(res.error) }));
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
  // A login revoked server side still reads as connected (`codex login status` and auth.json
  // are local), so signing in again has to be offered on the connected card. It signs in the
  // way the card was connected: the device flow for ChatGPT, the key form for an API key —
  // the device flow would silently switch an API-key workspace to a subscription.
  //
  // It does not sign out: the Agent runs the device flow under a staging home and replaces
  // auth.json only once the new sign-in completes, and the key route overwrites it only when a
  // key is submitted, so backing out of either keeps the current login. One run at a time
  // (reauthing): a second confirm while the first is in flight would open a second device flow.
  const reauthing = useRef(false);
  const reauth = async () => {
    if (reauthing.current) return;
    reauthing.current = true;
    try {
      if (st?.method === "apikey") setMode("key");
      else if (subAllowed) await startDevice();
    } finally {
      reauthing.current = false;
    }
  };
  const back = () => {
    activeFlow.current = null;
    setMode("idle");
    setDev(null);
    setKey("");
  };

  return (
    <ProviderCard
      id="codex"
      name={kindDisplayName("codex")}
      status={
        running ? (
          <StatusPill on={connected}>{connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : /* The flow before the connection state, as on the Claude card: re-auth opens it while
            the card is still connected. */
      mode === "device" && dev ? (
        <div className="p-body">
          <DeviceSteps code={dev.user_code} url={dev.url} status={dev.status} ended={dev.ended} />
          <Hint>
            {tr("agents.codex_hint1_1")}
            <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
              {tr("agents.codex_settings_security")}
            </a>
            {tr("agents.codex_hint1_2")}
          </Hint>
          <div className="flow">
            <button className="ghost" onClick={back}>
              {tr("common.back")}
            </button>
          </div>
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
            <button className="ghost" disabled={busy} onClick={back}>
              {tr("common.back")}
            </button>
          </div>
          <IssueLink url="https://platform.openai.com/api-keys" />
        </div>
      ) : connected && st ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || (st.method === "apikey" ? tr("agents.codex_apikey_label") : "ChatGPT")}
          </span>
          {st.plan && <span className="p-pl">{st.plan}</span>}
          <ReauthButton disabled={busy} onClick={() => void reauth()} />
          <DisconnectButton disabled={busy} onClick={disconnect} />
        </div>
      ) : (
        <>
          <div className="p-desc">
            {tr(subAllowed && keyAllowed ? "agents.codex_desc" : subAllowed ? "cred.codex_desc_sub_only" : "cred.codex_desc_key_only")}
          </div>
          <div className="p-body">
            <div className="p-opts">
              {subAllowed && (
                <button type="button" className="p-opt" disabled={busy} onClick={startDevice}>
                  <span className="p-opt-t">
                    {tr("agents.codex_connect_sub")} <span className="p-rec">{tr("git.recommended")}</span>
                  </span>
                  <span className="p-opt-s">{tr("agents.codex_sub_note")}</span>
                </button>
              )}
              {keyAllowed && (
                <button type="button" className="p-opt" onClick={() => setMode("key")}>
                  <span className="p-opt-t">{tr("agents.codex_connect_key")}</span>
                  <span className="p-opt-s">{tr("agents.codex_key_note")}</span>
                </button>
              )}
            </div>
            {subAllowed && (
            <Hint>
              {tr("agents.codex_hint2_1")}
              <a href="https://chatgpt.com/#settings/Security" target="_blank" rel="noopener" className="flow-link">
                {tr("agents.codex_settings_security")}
              </a>
              {tr("agents.codex_hint2_2")}
            </Hint>
            )}
          </div>
        </>
      )}
    </ProviderCard>
  );
}
