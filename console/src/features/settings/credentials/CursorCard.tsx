import { useState } from "react";
import { api, apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { useActiveFlow } from "../parts/useActiveFlow.ts";
import { usePolling } from "../parts/usePolling.ts";
import { ConnPaused, type DeviceFlow } from "../agents/AgentCardParts.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// Cursor: dedicated login flow (docs/log/40). `NO_OPEN_BROWSER=1 cursor-agent login`
// prints an authorize URL and self-polls until the user approves in a browser, then
// writes ~/.config/cursor/auth.json — so the UI shows the URL and polls
// api/connections/cursor/poll (no pasted code, unlike Claude/Codex). v1 is
// login-only; a manual CURSOR_API_KEY registration path is deferred to Track D
// (cursor has no key-persistence command and injecting it into the TUI pane would
// leak it into `ps`).
export function CursorCard({ running, st, reload }: { running: boolean; st: ProviderConn | undefined; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const active = useActiveFlow();
  const [flow, setFlow] = useState<DeviceFlow | null>(null);
  const [busy, setBusy] = useState(false);
  const unsupported = st?.supported === false;

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/cursor/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.cursor_auth_failed", { msg: res?.error ? errDetail(res.error) : "" }));
        return;
      }
      active.begin(res.flow_id);
      setFlow({ url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 10 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => {
          if (active.is(res.flow_id)) setFlow((f) => (f ? { ...f, status: tr("git.oauth_expired"), ended: true } : f));
        },
        step: async () => {
          if (!active.is(res.flow_id)) return { stop: true };
          let p;
          try {
            p = await apiJSON("api/connections/cursor/poll", "POST", { flow_id: res.flow_id });
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
    await raw("api/connections/cursor", { method: "DELETE" });
    active.end();
    setFlow(null);
    reload();
  };

  return (
    <ProviderCard
      id="cursor"
      name={kindDisplayName("cursor")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : unsupported ? (
        <div className="p-desc">{tr("agents.cursor_unsupported", { reason: st?.reason || "" })}</div>
      ) : flow ? (
        // Before the connection state: re-auth opens the flow while the card is still connected.
        <div className="p-body">
          {/* No pasted code — cursor approves entirely in the browser, then self-polls. */}
          <DeviceSteps url={flow.url} status={flow.status} ended={flow.ended} />
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
            {st.email || "Cursor"}
          </span>
          <ReauthButton disabled={busy} onClick={() => void startLogin()} />
          <DisconnectButton disabled={busy} onClick={disconnect} />
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.cursor_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                <span className="p-opt-t">{tr("agents.cursor_connect")}</span>
                <span className="p-opt-s">{tr("agents.cursor_connect_note")}</span>
              </button>
            </div>
            <Hint>
              {tr("agents.cursor_hint_1")}
              <a href="https://cursor.com/dashboard" target="_blank" rel="noopener" className="flow-link">
                {tr("agents.cursor_dashboard")}
              </a>
              {tr("agents.cursor_hint_2")}
            </Hint>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
