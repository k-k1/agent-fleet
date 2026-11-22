import { useState } from "react";
import { errDetail, raw } from "../../../core/api/client.ts";
import { startMuseLogin, pollMuseLogin, putMuseApiKey } from "./credentialsApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { ProviderCard, StatusPill, Hint, DeviceSteps, DisconnectButton, ReauthButton } from "../parts/providerCard.tsx";
import { useActiveFlow } from "../parts/useActiveFlow.ts";
import { usePolling } from "../parts/usePolling.ts";
import { ConnPaused, type DeviceFlow } from "../agents/AgentCardParts.tsx";
import { NotInstalled } from "./NotInstalled.tsx";
import type { ProviderConn } from "../../../types/session.ts";

// Muse Code (ADR 0095). Three states, and the order matters because each is a precondition of
// the next: the proprietary binary is not in the image, so a fresh workspace points to the install
// on the Agents page card (ADR 0138 D16); once installed it offers the device-code sign-in; once
// signed in it shows who, on which credential, and the disconnect.
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
// The launch defaults block carries the model and the reasoning effort only. Its permission
// choice is deliberately absent (approvals cannot fire — registry.ts), and muse has no plan
// mode, so those two rows would be controls with nothing behind them.
export function MuseCard({
  running,
  st: rawSt,
  reload,
  allowed,
}: {
  running: boolean;
  st: ProviderConn | undefined;
  reload: () => void;
  /** The routes the policy allows muse (ADR 0138 D10a). */
  allowed: Set<string>;
}) {
  const tr = useT();
  // Each control is judged by the route it stores a credential for: the account sign-in by
  // muse-login, a pasted key by meta-api. A stored credential on a forbidden route is shown as not
  // connected (D14a): `metered` is what tells a key from a sign-in.
  const loginAllowed = allowed.has("muse-login");
  const keyAllowed = allowed.has("meta-api");
  const storedAllowed = rawSt?.metered ? keyAllowed : loginAllowed;
  const st = rawSt && rawSt.connected && !storedAllowed ? { ...rawSt, connected: false, metered: false, email: undefined } : rawSt;
  const toast = useToast();
  const poll = usePolling();
  const active = useActiveFlow();
  const [flow, setFlow] = useState<DeviceFlow | null>(null);
  const [mode, setMode] = useState<"idle" | "key">("idle");
  const [key, setKey] = useState("");
  // The key form was opened by re-authentication, so it may show over a status that still reads
  // connected (the refetch has not landed). Without this flag a login made elsewhere while the
  // ordinary key form is open would be hidden behind the form.
  const [keyReauth, setKeyReauth] = useState(false);
  const [busy, setBusy] = useState(false);
  const unsupported = st?.supported === false; // the CLI is not installed yet (installed on the Agents page)

  const startLogin = async () => {
    setBusy(true);
    try {
      const res = await startMuseLogin();
      if (!res || res.error || !res.url) {
        toast(tr("agents.muse_auth_failed", { msg: res?.error ? errDetail(res.error) : "" }));
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
            p = await pollMuseLogin(res.flow_id);
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
          // The sign-in child exited without writing a credential — an expired code, a refused
          // approval. Saying so ends the wait instead of spinning to the 15-minute deadline for
          // something that can no longer happen (the Agent reports it; see auth.go's HandlePoll).
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

  const saveKey = async () => {
    setBusy(true);
    try {
      const res = await putMuseApiKey(key.trim());
      if (res && res.error) {
        toast(tr("conn.connect_failed", { msg: errDetail(res.error) }));
        return;
      }
      setKey("");
      setMode("idle");
      setKeyReauth(false);
      reload();
    } finally {
      setBusy(false);
    }
  };

  // Signs in the way the card was connected: the device flow for an account sign-in, the key
  // form for a pasted API key (`metered`). The device flow would silently move a metered
  // workspace onto the subscription. Neither signs out first: the device flow signs in under a
  // staging home and replaces the login only when it completes, and a key replaces a key only
  // when one is submitted.
  const reauthMuse = async () => {
    if (st?.metered && keyAllowed) {
      setKeyReauth(true);
      setMode("key");
    } else if (loginAllowed) await startLogin();
  };
  const disconnect = async () => {
    await raw("api/connections/muse", { method: "DELETE" });
    active.end();
    setFlow(null);
    setMode("idle");
    setKeyReauth(false);
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
      ) : flow ? (
        // Before the connection state: re-auth opens the flow while the card is still connected.
        <div className="p-body">
          {/* Device flow: a URL with the code embedded plus the code on its own, to compare in the
              browser. muse polls Meta itself, so there is nothing to paste back. */}
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
      ) : mode === "key" && (!st?.connected || keyReauth) ? (
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
            <button
              className="ghost"
              onClick={() => {
                setMode("idle");
                setKeyReauth(false);
              }}
            >
              {tr("common.back")}
            </button>
          </div>
        </div>
      ) : st?.connected ? (
        <>
          <div className="p-who">
            <span className="p-em" title={st.email || ""}>
              {st.email || st.name || "Muse Code"}
            </span>
            <ReauthButton disabled={busy} onClick={() => void reauthMuse()} />
            <DisconnectButton disabled={busy} onClick={disconnect} />
          </div>
          {/* Which credential, and what it costs. Only said when it is NOT the subscription: a
              plain account sign-in needs no note, and a note on every card teaches nothing. */}
          {st.metered && <p className="ps-note ps-note-warn p-card-note">{tr("agents.muse_metered")}</p>}
          {st.env_key && <p className="ps-note ps-note-warn p-card-note">{tr("agents.muse_env_key")}</p>}
        </>
      ) : unsupported ? (
        <NotInstalled kind="muse" />
      ) : (
        <>
          <div className="p-desc">{tr(loginAllowed ? "agents.muse_desc" : "cred.muse_desc_key_only")}</div>
          <div className="p-body">
            <div className="p-opts">
              {loginAllowed && (
                <button type="button" className="p-opt" disabled={busy} onClick={startLogin}>
                  <span className="p-opt-t">
                    {tr("agents.muse_connect")} <span className="p-rec">{tr("git.recommended")}</span>
                  </span>
                  <span className="p-opt-s">{tr("agents.muse_connect_note")}</span>
                </button>
              )}
              {keyAllowed && (
                <button type="button" className="p-opt" onClick={() => setMode("key")}>
                  <span className="p-opt-t">{tr("agents.muse_use_key")}</span>
                  <span className="p-opt-s">{tr("agents.muse_use_key_note")}</span>
                </button>
              )}
            </div>
            <Hint>{tr("agents.muse_hint")}</Hint>
          </div>
        </>
      )}
    </ProviderCard>
  );
}
