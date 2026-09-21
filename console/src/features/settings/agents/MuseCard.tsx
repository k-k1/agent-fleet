import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail, raw } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton } from "../parts/providerCard.tsx";
import { usePolling } from "../parts/usePolling.ts";
import { ConnPaused } from "./AgentCardParts.tsx";

// Muse Code (ADR 0095). Three states, and the order matters because each is a precondition of
// the next: the proprietary binary is not in the image, so a fresh workspace offers an install
// (~299MB into the member's home); once installed it offers the device-code sign-in; once signed
// in it shows who, on which credential, and the disconnect.
//
// 🔴 What makes this card different from every other agent card is that "connected" is not the
// whole truth. Three facts the Agent reports have to reach the screen or the member is misled
// about their own bill (ADR 0095 decision 9 / P2-6, all measured):
//
//   - `metered` — an account sign-in rides the subscription; a pasted API key bills per use. It
//     cannot be inferred here: muse's device-code login writes an `api_key` of its own alongside
//     the access token, so a card guessing from the key's presence would tell every subscription
//     member they were being billed per use. The Agent derives it from muse's own `mechanism`.
//   - `env_key` — META_API_KEY in the workspace environment OVERRIDES the stored sign-in, so
//     connected can be true, metered false, and the member still billed per use. AF reports it
//     rather than stripping it (a deployment may mean it), so the card is where the contradiction
//     becomes visible.
//   - The API-key route REFUSES while an account sign-in exists, rather than warning: `muse auth
//     set` replaces the whole provider entry, so it would destroy the sign-in as well as move the
//     member onto metered billing. The card says that up front instead of letting a 409 be the
//     first the member hears of it.
//
// No LaunchDefaults block yet: muse's model and effort controls are their own work package, and
// its permission choice is deliberately absent (approvals cannot fire — registry.ts). A settings
// group whose only row is an inert "Default" picker is worse than none.
export function MuseCard({ running, st, reload }: { running: boolean; st: any; reload: () => void }) {
  const tr = useT();
  const toast = useToast();
  const poll = usePolling();
  const [flow, setFlow] = useState<any>(null); // { url, user_code, flow_id, status } while signing in
  const [installing, setInstalling] = useState<null | "installing" | "error">(null);
  const [mode, setMode] = useState<"idle" | "key">("idle");
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  // { installed, version, pin, updateAvailable } — the version facts behind the update
  // affordance. muse is managed-only, so unlike kiro there is no launch guard to re-pin
  // implicitly: this card is the ONLY place a pin bump or a self-installed shadow gets repaired.
  const [inst, setInst] = useState<any>(null);
  const unsupported = st?.supported === false; // the binary is not installed yet

  const loadInstall = useCallback(async () => {
    if (!running) return;
    try {
      setInst(await api("api/connections/muse/install"));
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
      const res = await api("api/connections/muse/install", { method: "POST" });
      if (!res || res.error) {
        setInstalling("error");
        toast(tr("agents.muse_install_failed", { msg: res?.error ? errDetail(res.error) : "" }));
        return;
      }
      if (res.state === "done") {
        setInstalling(null);
        void loadInstall();
        reload();
        return;
      }
      poll({
        deadlineMs: 20 * 60 * 1000,
        firstDelayMs: 4000,
        onExpire: () => setInstalling("error"),
        step: async () => {
          let p;
          try {
            p = await api("api/connections/muse/install");
          } catch {
            p = null;
          }
          if (p && p.state === "done") {
            setInstalling(null);
            void loadInstall();
            reload();
            return { stop: true };
          }
          if (p && p.state === "error") {
            setInstalling("error");
            toast(tr("agents.muse_install_failed", { msg: p.error || "" }));
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
      const res = await api("api/connections/muse/start", { method: "POST" });
      if (!res || res.error || !res.url) {
        toast(tr("agents.muse_auth_failed", { msg: res?.error ? errDetail(res.error) : "" }));
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
            p = await apiJSON("api/connections/muse/poll", "POST", { flow_id: res.flow_id });
          } catch {
            p = null;
          }
          if (p && p.connected) {
            setFlow(null);
            reload();
            return { stop: true };
          }
          // The sign-in child exited without writing a credential — an expired code, a refused
          // approval. Saying so ends the wait instead of spinning to the 15-minute deadline for
          // something that can no longer happen (the Agent reports it; see auth.go's HandlePoll).
          if (p && p.failed) {
            setFlow((f: any) => (f ? { ...f, status: tr("git.oauth_expired") } : f));
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
    setBusy(true);
    try {
      const res = await apiJSON("api/connections/muse/api-key", "POST", { key: key.trim() });
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
    await raw("api/connections/muse", { method: "DELETE" });
    setFlow(null);
    setMode("idle");
    reload();
  };

  return (
    <ProviderCard
      id="muse"
      name={kindDisplayName("muse")}
      status={
        running ? (
          <StatusPill on={st?.connected}>{st?.connected ? tr("conn.connected") : tr("conn.disconnected")}</StatusPill>
        ) : undefined
      }
    >
      {!running ? (
        <ConnPaused />
      ) : st?.connected ? (
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || ""}>
              {st.email || st.name || "Muse Code"}
            </span>
            <DisconnectButton onClick={disconnect} />
          </div>
          {/* Which credential, and what it costs. Only said when it is NOT the subscription: a
              plain account sign-in needs no note, and a note on every card teaches nothing. */}
          {st.metered && <p className="ps-note ps-note-warn">{tr("agents.muse_metered")}</p>}
          {st.env_key && <p className="ps-note ps-note-warn">{tr("agents.muse_env_key")}</p>}
        </>
      ) : unsupported ? (
        <>
          <div className="p-desc">{tr("agents.muse_install_desc")}</div>
          <div className="p-body">
            {installing === "installing" ? (
              <p className="ps-note ps-note-warn">{tr("agents.muse_installing")}</p>
            ) : (
              <>
                <div className="p-opts">
                  <button type="button" className="p-opt" disabled={busy} onClick={install}>
                    <span className="p-opt-t">{tr("agents.muse_install")}</span>
                    <span className="p-opt-s">{tr("agents.muse_install_note")}</span>
                  </button>
                </div>
                {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.muse_install_error")}</p>}
              </>
            )}
          </div>
        </>
      ) : flow ? (
        <div className="p-body">
          {/* Device flow: a URL with the code embedded plus the code on its own, to compare in the
              browser. muse polls Meta itself, so there is nothing to paste back. */}
          <DeviceSteps code={flow.user_code} url={flow.url} status={flow.status} />
        </div>
      ) : mode === "key" ? (
        <div className="p-body">
          {/* 🔴 The warning comes BEFORE the field, not after a failure: this route destroys an
              account sign-in and moves the workspace onto metered billing, and the member should
              read that while deciding rather than in a toast. */}
          <p className="ps-note ps-note-warn">{tr("agents.muse_key_warn")}</p>
          <div className="flow">
            <input
              className="cinput"
              type="password"
              placeholder={tr("agents.muse_key_placeholder")}
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
        </div>
      ) : (
        <>
          <div className="p-desc">{tr("agents.muse_desc")}</div>
          <div className="p-body">
            <div className="p-opts">
              <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                <span className="p-opt-t">
                  {tr("agents.muse_connect")} <span className="p-rec">{tr("git.recommended")}</span>
                </span>
                <span className="p-opt-s">{tr("agents.muse_connect_note")}</span>
              </button>
              <button type="button" className="p-opt" onClick={() => setMode("key")}>
                <span className="p-opt-t">{tr("agents.muse_use_key")}</span>
                <span className="p-opt-s">{tr("agents.muse_use_key_note")}</span>
              </button>
            </div>
            <Hint>{tr("agents.muse_hint")}</Hint>
          </div>
        </>
      )}
      {/* The update / shadow-repair affordance. Outside the branches above because it is about the
          BINARY, not the credential — and shown only when the Agent positively reports a version
          mismatch, so an unreadable version or a missing pin never nags the member into a 299MB
          download on a guess. It also covers the case decision 8 calls the shadow: a member who
          ran the vendor's own installer has an unmanaged, self-updating build at the same path,
          which shows up here as a version that differs from the pin. */}
      {running && !unsupported && inst?.updateAvailable && (
        <div className="p-body">
          {installing === "installing" ? (
            <p className="ps-note ps-note-warn">{tr("agents.muse_updating")}</p>
          ) : (
            <>
              <p className="ps-note ps-note-warn">
                {tr("agents.muse_update_avail", { cur: inst.version || "?", pin: inst.pin || "?" })}
              </p>
              <div className="p-opts">
                <button type="button" className="p-opt" disabled={busy} onClick={install}>
                  <span className="p-opt-t">{tr("agents.muse_update")}</span>
                  <span className="p-opt-s">{tr("agents.muse_update_note")}</span>
                </button>
              </div>
              {installing === "error" && <p className="ps-note ps-note-warn">{tr("agents.muse_install_error")}</p>}
            </>
          )}
        </div>
      )}
    </ProviderCard>
  );
}
