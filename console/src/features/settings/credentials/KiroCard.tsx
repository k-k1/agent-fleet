import { useState } from "react";
import { errDetail, raw } from "../../../core/api/client.ts";
import { startKiroLogin, pollKiroLogin } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { useActiveFlow } from "../parts/useActiveFlow.ts";
import { usePolling } from "../parts/usePolling.ts";
import { ConnPaused, type DeviceFlow } from "../agents/AgentCardParts.tsx";
import { NotInstalled } from "./NotInstalled.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// Kiro: device-flow login (docs/log/43 Track C). The ~855MB CLI is not baked on the lean image,
// so a fresh workspace reports supported=false; the install and update controls are on the Agents
// page card (ADR 0138 D16) and this card points there. Once installed, `kiro-cli login --license
// free --use-device-flow` prints a verification URL (+ short confirmation code) and self-polls AWS
// SSO until the user approves in a browser, so the UI shows both and polls
// api/connections/kiro/poll (no pasted code, like Codex/Cursor). v1 is login-only (Builder ID /
// free); the API-key path (KIRO_API_KEY, Pro+) is deferred to Track D.
export function KiroCard({ running, st, reload }: { running: boolean; st: ProviderConn | undefined; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const active = useActiveFlow();
  const [flow, setFlow] = useState<DeviceFlow | null>(null);
  const [busy, setBusy] = useState(false);
  const unsupported = st?.supported === false; // the CLI is not installed yet (installed on the Agents page)

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await startKiroLogin();
      if (!res || res.error || !res.url) {
        toast(tr("agents.kiro_auth_failed", { msg: res?.error ? errDetail(res.error) : "" }));
        return;
      }
      active.begin(res.flow_id);
      setFlow({ url: res.url, user_code: res.user_code, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => {
          if (active.is(res.flow_id)) setFlow((f) => (f ? { ...f, status: tr("git.oauth_expired"), ended: true } : f));
        },
        step: async () => {
          if (!active.is(res.flow_id)) return { stop: true };
          let p;
          try {
            p = await pollKiroLogin(res.flow_id);
          } catch {
            p = null;
          }
          if (!active.is(res.flow_id)) return { stop: true };
          if (p && p.connected) {
            active.end();
            setFlow(null);
            reload();
            return { stop: true };
          }
          // The sign-in child exited without signing in (an expired code, a refused approval):
          // nothing more can arrive, so end the wait instead of spinning to the deadline.
          // An error answer means the Agent took the flow and could not install it: nothing
          // more will come for this flow either.
          if (p && (p.failed || p.error)) {
            active.end();
            const msg = p.failed ? p.detail || "" : errDetail(p.error);
            const status = msg ? tr("provider.login_ended_detail", { msg }) : tr("provider.login_ended");
            setFlow((f) => (f ? { ...f, status, ended: true } : f));
            return { stop: true };
          }
          return { stop: false, nextMs: 2500 };
        },
      });
    } finally {
      setBusy(false);
    }
  };
  const disconnect = async () => {
    await raw("api/connections/kiro", { method: "DELETE" });
    active.end();
    setFlow(null);
    reload();
  };

  return (
    <ProviderCard
      id="kiro"
      name={kindDisplayName("kiro")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : flow ? (
        // Before the connection state: re-auth opens the flow while the card is still connected.
        <div className="p-body">
          {/* device flow: URL + a short code to confirm in the browser; kiro self-polls. */}
          <DeviceSteps code={flow.user_code} url={flow.url} status={flow.status} ended={flow.ended} />
          <div className="flow">
            <button
              className="ghost"
              onClick={() => {
                active.end();
                setFlow(null);
              }}
            >
              {tr("common.cancel")}
            </button>
          </div>
        </div>
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || "Kiro"}
          </span>
          <ReauthButton disabled={busy} onClick={() => void startLogin()} />
          <DisconnectButton disabled={busy} onClick={disconnect} />
        </div>
      ) : unsupported ? (
        <NotInstalled kind="kiro" />
      ) : (
        <>
          <div className="p-desc">{tr("agents.kiro_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                <span className="p-opt-t">{tr("agents.kiro_connect")}</span>
                <span className="p-opt-s">{tr("agents.kiro_connect_note")}</span>
              </button>
            </div>
            <Hint>{tr("agents.kiro_hint")}</Hint>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
