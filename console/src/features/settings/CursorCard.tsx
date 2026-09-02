import { useState } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.tsx";
import { usePolling } from "./usePolling.ts";
import { CardSettings, ConnPaused, LaunchDefaults } from "./AgentCardParts.tsx";

// Cursor: dedicated login flow (docs/log/40). `NO_OPEN_BROWSER=1 cursor-agent login`
// prints an authorize URL and self-polls until the user approves in a browser, then
// writes ~/.config/cursor/auth.json — so the UI shows the URL and polls
// api/connections/cursor/poll (no pasted code, unlike Claude/Codex). v1 is
// login-only; a manual CURSOR_API_KEY registration path is deferred to Track D
// (cursor has no key-persistence command and injecting it into the TUI pane would
// leak it into `ps`). No RTK toggle yet — cursor's rtk hook seam is Track D.
export function CursorCard({ running, st, reload }: { running: boolean; st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [flow, setFlow] = useState<any>(null); // { url, flow_id, status } while a login is in flight
  const [busy, setBusy] = useState(false);
  const unsupported = st?.supported === false;

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/cursor/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.cursor_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      setFlow({ url: res.url, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 10 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f)),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/cursor/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setFlow(null);
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
  const disconnect = async () => {
    await raw("api/connections/cursor", { method: "DELETE" });
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
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || "Cursor"}
          </span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : flow ? (
        <div className="p-body">
          {/* No pasted code — cursor approves entirely in the browser, then self-polls. */}
          <DeviceSteps url={flow.url} status={flow.status} />
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
      <CardSettings>
        <LaunchDefaults kind="cursor" />
      </CardSettings>
    </ProviderCard>
  );
}
