import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, raw } from "../../core/api/client.ts";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { kindDisplayName } from "../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "./providerCard.tsx";
import { usePolling } from "./usePolling.ts";
import { CardSettings, ConnPaused, LaunchDefaults } from "./AgentCardParts.tsx";

// Kiro: on-demand install + device-flow login (docs/log/43 Track C). Kiro's ~855MB
// bundle is NOT baked on the lean image (decision §4-2), so a fresh workspace reports
// supported=false; the card offers an "install" button that lands the CLI in the
// user's ~/.local (POST /connections/kiro/install runs in the background, we poll
// GET for progress). Once installed, `kiro-cli login --license free --use-device-flow`
// prints a verification URL (+ short confirmation code) and self-polls AWS SSO until
// the user approves in a browser — so the UI shows both and polls
// api/connections/kiro/poll (no pasted code, like Codex/Cursor). v1 is login-only
// (Builder ID / free); the API-key path (KIRO_API_KEY, Pro+) is deferred to Track D.
// No RTK toggle yet — kiro's rtk hook seam is Track D.
export function KiroCard({ running, st, reload }: { running: boolean; st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [flow, setFlow] = useState<any>(null); // { url, user_code, flow_id, status } while a login is in flight
  const [installing, setInstalling] = useState<null | "installing" | "error">(null);
  const [busy, setBusy] = useState(false);
  // { installed, version, pin, updateAvailable } — the version facts behind the update
  // affordance below. Kiro is the one CLI whose copy lives in the home volume with no
  // self-updater and no boot-install, so a versions.json pin bump only reaches it when
  // something re-installs. The launch guard does that implicitly at the next launch;
  // this makes it EXPLICIT (you see that an update exists and press the button when a
  // multi-minute download suits you, instead of being surprised mid-launch).
  const [inst, setInst] = useState<any>(null);
  const unsupported = st?.supported === false; // CLI not installed yet (on-demand)

  const loadInstall = useCallback(async () => {
    if (!running) return;
    try {
      setInst(await api("api/connections/kiro/install"));
    } catch {
      /* stopped workspace / transient 502 — the card just shows no update notice */
    }
  }, [running]);
  useEffect(() => {
    void loadInstall();
  }, [loadInstall, st?.supported]);

  const install = async () => {
    setBusy(true);
    setInstalling("installing");
    try {
      const res = await api("api/connections/kiro/install", { method: "POST" });
      if (!res || res.error) {
        setInstalling("error");
        toast(tr("agents.kiro_install_failed", { msg: res?.error?.message || "" }));
        return;
      }
      if (res.state === "done") {
        setInstalling(null);
        void loadInstall();
        reload();
        return;
      }
      // Poll the background install until it finishes; the ~855MB download is slow.
      poll({
        deadlineMs: 20 * 60 * 1000,
        firstDelayMs: 4000,
        onExpire: () => setInstalling("error"),
        step: async () => {
          let p;
          try {
            p = await api("api/connections/kiro/install");
          } catch {
            p = null;
          }
          if (p && p.state === "done") {
            setInstalling(null);
            void loadInstall(); // refresh version / updateAvailable after an upgrade
            reload();
            return { stop: true };
          }
          if (p && p.state === "error") {
            setInstalling("error");
            toast(tr("agents.kiro_install_failed", { msg: p.error || "" }));
            return { stop: true };
          }
          return { stop: false, nextMs: 4000 };
        },
      });
    } finally {
      setBusy(false);
    }
  };

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await api("api/connections/kiro/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.kiro_auth_failed", { msg: res?.error?.message || "" }));
        return;
      }
      setFlow({ url: res.url, user_code: res.user_code, flow_id: res.flow_id, status: tr("git.oauth_waiting") });
      poll({
        deadlineMs: 15 * 60 * 1000,
        firstDelayMs: 3000,
        onExpire: () => setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f)),
        step: async () => {
          let p;
          try {
            p = await apiJSON("api/connections/kiro/poll", "POST", { flow_id: res.flow_id });
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
    await raw("api/connections/kiro", { method: "DELETE" });
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
      ) : st?.connected ? (
        <div className="p-who">
          <span className="p-em" title={st.email || ""}>
            {st.email || "Kiro"}
          </span>
          <DisconnectButton onClick={disconnect} />
        </div>
      ) : unsupported ? (
        // Not installed yet — offer the on-demand install (~855MB into the home volume).
        <>
          <div className="p-desc">{tr("agents.kiro_install_desc")}</div>
          <div className="p-body">
            {installing === "installing" ? (
              <p className="ps-note ps-note-warn">{tr("agents.kiro_installing")}</p>
            ) : (
              <>
                <div className="p-opts">
                  <button type="button" className="p-opt" disabled={busy} onClick={install}>
                    <span className="p-opt-t">{tr("agents.kiro_install")}</span>
                    <span className="p-opt-s">{tr("agents.kiro_install_note")}</span>
                  </button>
                </div>
                {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.kiro_install_error")}</p>}
              </>
            )}
          </div>
        </>
      ) : flow ? (
        <div className="p-body">
          {/* device flow: URL + a short code to confirm in the browser; kiro self-polls. */}
          <DeviceSteps code={flow.user_code} url={flow.url} status={flow.status} />
        </div>
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
      {/* Update affordance. Rendered outside the connection branches above because it
          applies whether or not you are signed in — it is about the BINARY, not the
          auth. Shown only when the agent positively reports a version mismatch against
          the versions.json pin (an unreadable version or a missing pin says nothing, so
          the user is never nagged into a 554MB download on a guess). */}
      {running && !unsupported && inst?.updateAvailable && (
        <div className="p-body">
          {installing === "installing" ? (
            <p className="ps-note ps-note-warn">{tr("agents.kiro_updating")}</p>
          ) : (
            <>
              <p className="ps-note ps-note-warn">
                {tr("agents.kiro_update_avail", { cur: inst.version || "?", pin: inst.pin || "?" })}
              </p>
              <div className="p-opts">
                <button type="button" className="p-opt" disabled={busy} onClick={install}>
                  <span className="p-opt-t">{tr("agents.kiro_update")}</span>
                  <span className="p-opt-s">{tr("agents.kiro_update_note")}</span>
                </button>
              </div>
              {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.kiro_install_error")}</p>}
            </>
          )}
        </div>
      )}
      <CardSettings>
        <LaunchDefaults kind="kiro" />
      </CardSettings>
    </ProviderCard>
  );
}
