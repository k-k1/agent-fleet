import { useCallback, useEffect, useRef, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { McpServer } from "./mcpWire.ts";
import { disconnectOAuth, fetchOAuthStatus, saveClientId, startSignIn } from "./mcpOAuthApi.ts";
import { stepUpRequest } from "./mcpOAuth.ts";
import type { OAuthRegistration, OAuthReturn, OAuthState, OAuthStatus } from "./mcpOAuth.ts";
import { clearSameTab, markSameTab, onSignInDone, outcomeText } from "./mcpOAuthSignal.ts";

// openSignInTab opens the tab the authorization server will be shown in. It has to run in the
// click's own task: the authorization URL only exists after an async start call, and a window
// opened after that await is popup-blocked (Safari in particular). The tab starts blank and is
// pointed at the URL later; opener is cut by hand because noopener would make window.open
// return null, leaving nothing to navigate. Null means the browser blocked it.
function openSignInTab(): Window | null {
  clearSameTab();
  let w: Window | null = null;
  try {
    w = window.open("about:blank", "_blank");
  } catch {
    return null;
  }
  if (w) {
    try {
      w.opener = null;
    } catch {
      /* cross-origin opener is read-only in some engines; the tab still works */
    }
  }
  return w;
}

// useOAuthFlow is the sign-in state of one OAuth server row (ADR 0119 P2): the Agent's status,
// and the actions that change it. The row, its panel and the connection-test report share one
// instance, so a sign-in started from the report is the same one the panel shows.
export function useOAuthFlow(s: McpServer, failedReturn?: OAuthReturn | null) {
  const tr = useT();
  const toast = useToast();
  const askConfirm = useConfirm();
  const active = s.auth === "oauth" && s.transport === "http";
  const [status, setStatus] = useState<OAuthStatus | null>(null);
  const [statusErr, setStatusErr] = useState("");
  const [busy, setBusy] = useState(false);
  // Set by a 409 client_id_needed: the issuer a client id has to be entered for.
  const [clientFor, setClientFor] = useState<string | null>(null);
  const seq = useRef(0);
  // True while a sign-in started from this tab has not been seen to finish: only then does a
  // window focus re-read the status (a row per OAuth server would otherwise read on every focus).
  const awaiting = useRef(false);
  // Guards double clicks: React's busy state is not visible to a second click in the same tick.
  const inFlight = useRef(false);

  // The registration path the sign-in started from this tab used; a failed finish of a CIMD one
  // keeps the DCR retry on offer even though the Agent has already consumed its pending record.
  const startedReg = useRef<OAuthRegistration | undefined>(undefined);
  const [failedCimd, setFailedCimd] = useState(false);
  // A completion notice is being checked against the status; repeats meanwhile are dropped.
  const checking = useRef(false);

  const reload = useCallback(async (): Promise<OAuthStatus | null> => {
    if (!active) return null;
    const mine = ++seq.current;
    const r = await fetchOAuthStatus(s.id);
    if (mine !== seq.current) return null; // a newer read is in flight
    if ("error" in r) {
      setStatusErr(errText(r.error as any));
      return null;
    }
    setStatusErr("");
    setStatus(r);
    if (r.state === "connected") awaiting.current = false;
    return r;
  }, [active, s.id]);
  useEffect(() => {
    void reload();
  }, [reload]);

  // The sign-in finishes in another tab. Only that tab's outcome for THIS server is acted on.
  useEffect(() => {
    if (!active) return;
    // A notice is a hint, never proof: it is acted on only while a sign-in started from this tab
    // is awaited (anyone can open a made-up return URL in another tab), and "Signed in" is said
    // only once the status read agrees.
    const off = onSignInDone((d) => {
      if (d.server !== s.id || !awaiting.current || checking.current) return;
      checking.current = true;
      void (async () => {
        try {
          const st = await reload();
          if (d.outcome === "ok") {
            if (st?.state === "connected") toast(outcomeText(tr, "ok"));
            return; // not connected yet: keep waiting, the focus re-read tries again
          }
          // A failure is confirmed the same way: the callback consumes the Agent's pending record
          // when it fails, so a record still there (or an unreadable status, or a connection)
          // means the notice is made up or belongs to an earlier attempt.
          if (!st || st.state === "connected" || st.pendingRegistration) return;
          awaiting.current = false;
          if (startedReg.current === "cimd") setFailedCimd(true);
          toast(outcomeText(tr, d.outcome));
        } finally {
          checking.current = false;
        }
      })();
    });
    const wake = () => {
      if (awaiting.current && document.visibilityState !== "hidden") void reload();
    };
    window.addEventListener("focus", wake);
    document.addEventListener("visibilitychange", wake);
    return () => {
      off();
      window.removeEventListener("focus", wake);
      document.removeEventListener("visibilitychange", wake);
    };
  }, [active, s.id, reload, toast, tr]);

  // Opens the authorization server in a new tab, so the Console stays where it is. `pre` is a
  // tab the caller already opened inside its own click; without one this opens it, which only
  // works when signIn runs synchronously from the click. A blocked tab falls back to this tab.
  const signIn = useCallback(
    async (opts: { registration?: "dcr" | "cimd" | "preregistered"; scope?: string } = {}, pre?: Window | null) => {
      if (inFlight.current) {
        pre?.close();
        return;
      }
      inFlight.current = true;
      setFailedCimd(false);
      const tab = pre === undefined ? openSignInTab() : pre;
      setBusy(true);
      try {
        const r = await startSignIn(s.id, opts);
        if ("error" in r) {
          tab?.close();
          if (r.error.code === "client_id_needed" && r.error.issuer) {
            setClientFor(r.error.issuer);
            return;
          }
          toast(tr("mcp.oauth_signin_failed", { msg: errText(r.error as any) }));
          return;
        }
        startedReg.current = r.registration;
        if (tab) {
          if (tab.closed) return; // the member closed it: they changed their mind
          tab.location.href = r.authorizeUrl;
          awaiting.current = true;
          toast(tr("mcp.oauth_newtab_note"));
          return;
        }
        toast(tr("mcp.oauth_popup_blocked"));
        markSameTab();
        window.location.assign(r.authorizeUrl);
      } catch (e) {
        tab?.close();
        toast(tr("mcp.oauth_signin_failed", { msg: errText(e as any) }));
      } finally {
        inFlight.current = false;
        setBusy(false);
      }
    },
    [s.id, toast, tr],
  );

  const submitClient = useCallback(
    async (clientId: string, secret: string) => {
      if (!clientFor || inFlight.current) return;
      inFlight.current = true;
      const tab = openSignInTab(); // still inside the click: the save below would outlive it
      setBusy(true);
      let saved = false;
      try {
        const r = await saveClientId(s.id, clientFor, clientId, secret);
        if (r.error) {
          toast(tr("mcp.save_failed", { msg: errText(r.error as any) }));
          return;
        }
        setClientFor(null);
        saved = true;
      } finally {
        inFlight.current = false;
        setBusy(false);
        if (!saved) tab?.close();
      }
      await signIn({}, tab); // the stored client id is first in the registration order
    },
    [clientFor, s.id, signIn, toast, tr],
  );

  const disconnect = useCallback(async () => {
    const ok = await askConfirm({
      title: tr("mcp.oauth_disc_title", { name: s.label || s.name }),
      body: tr("mcp.oauth_disc_body"),
      confirmLabel: tr("mcp.oauth_disc_confirm"),
      danger: true,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const r = await disconnectOAuth(s.id);
      if (r.error) {
        toast(tr("mcp.save_failed", { msg: errText(r.error as any) }));
        return;
      }
      toast(tr("mcp.oauth_disconnected", { name: s.label || s.name }));
      await reload();
    } finally {
      setBusy(false);
    }
  }, [askConfirm, reload, s.id, s.label, s.name, toast, tr]);

  // The DCR retry is offered after a CIMD sign-in that did not finish: either the redirect said
  // so, or the Agent still holds the unfinished attempt. Not when connected.
  const cimdUnfinished =
    status?.state !== "connected" &&
    (status?.pendingRegistration === "cimd" || failedCimd || (failedReturn?.server === s.id && failedReturn.registration === "cimd"));

  // A sign-in the Agent still holds and nothing came back for (the authorization server may have
  // refused the client on its own error page, where it cannot redirect to us).
  const unfinished = status?.state !== "connected" && !!status?.pendingRegistration;

  return { active, status, unfinished, statusErr, busy, clientFor, setClientFor, signIn, submitClient, disconnect, reload, cimdUnfinished };
}

export type OAuthFlow = ReturnType<typeof useOAuthFlow>;

const STATE_LABEL = {
  not_signed_in: "mcp.oauth_state_not_signed_in",
  connected: "mcp.oauth_state_connected",
  sign_in_again: "mcp.oauth_state_sign_in_again",
  needs_more_permission: "mcp.oauth_state_needs_more_permission",
} as const satisfies Record<OAuthState, string>;

const REG_LABEL = {
  preregistered: "mcp.oauth_reg_path_preregistered",
  cimd: "mcp.oauth_reg_cimd",
  dcr: "mcp.oauth_reg_dcr",
} as const satisfies Record<OAuthRegistration, string>;

const RELAY_NOTE = {
  not_signed_in: "mcp.oauth_relay_note_not_signed_in",
  connected: "mcp.oauth_relay_note_connected",
  sign_in_again: "mcp.oauth_relay_note_again",
  needs_more_permission: "mcp.oauth_relay_note_connected",
} as const satisfies Record<OAuthState, string>;

// OAuthPanel is the sign-in block of an OAuth row: state, what was granted, and the buttons.
export function OAuthPanel({ flow }: { flow: OAuthFlow }) {
  const tr = useT();
  const { status, statusErr, busy, clientFor } = flow;
  if (!flow.active) return null;
  const state = status?.state ?? "not_signed_in";
  const signedIn = state === "connected" || state === "sign_in_again" || state === "needs_more_permission";
  const again = state === "sign_in_again";
  const stepUp = stepUpRequest(status);
  return (
    <div className={"mcp-oauth mcp-oauth-" + state} data-testid="mcp-oauth-panel" data-state={state}>
      <div className="mcp-oauth-head">
        <span className="mcp-oauth-state">{tr(STATE_LABEL[state])}</span>
        <span className="mcp-actions">
          {/* Connected needs no Sign in beside "Connected": Disconnect, then Sign in, switches account. */}
          {state === "needs_more_permission" && (
            // The relay recorded the scope the server challenged for; the new sign-in asks for it
            // on top of what is already granted, since the new grant replaces the old one. It is the
            // only sign-in button here: the Agent adds the recorded scope to a scope-less start
            // itself, so a plain Sign in again would be the same request without this check.
            <button
              className="primary mcp-btn"
              disabled={busy || !stepUp.ok}
              onClick={() => stepUp.ok && void flow.signIn({ scope: stepUp.scope })}
            >
              {tr("mcp.oauth_btn_stepup")}
            </button>
          )}
          {state !== "connected" && state !== "needs_more_permission" && (
            <button className="primary mcp-btn" disabled={busy} onClick={() => void flow.signIn()}>
              {again ? tr("mcp.oauth_btn_signin_again") : tr("mcp.oauth_btn_signin")}
            </button>
          )}
          {signedIn && (
            <button className="ghost danger mcp-btn" disabled={busy} onClick={() => void flow.disconnect()}>
              {tr("mcp.oauth_btn_disconnect")}
            </button>
          )}
        </span>
      </div>
      {statusErr && (
        <div className="ps-note ps-note-warn" data-testid="mcp-oauth-status-error">
          <div>{tr("mcp.oauth_status_failed", { msg: statusErr })}</div>
          <button className="ghost mcp-btn" onClick={() => void flow.reload()}>
            {tr("mcp.oauth_btn_reload")}
          </button>
        </div>
      )}
      {state === "connected" && (
        <div className="muted">
          {status?.expiresAt ? <div>{tr("mcp.oauth_expires", { when: fmtDateTime(status.expiresAt * 1000, DATETIME_FULL) })}</div> : null}
          {status?.scopes && status.scopes.length > 0 && (
            <div className="mono">{tr("mcp.oauth_granted_scopes", { scopes: status.scopes.join(" ") })}</div>
          )}
        </div>
      )}
      {state === "sign_in_again" && <p className="ps-note ps-note-warn">{tr("mcp.oauth_again_note")}</p>}
      {state === "needs_more_permission" && (
        <p className="ps-note ps-note-warn">{tr("mcp.oauth_step_note", { scope: status?.stepUpScope || "?" })}</p>
      )}
      {state === "needs_more_permission" && !stepUp.ok && (
        <p className="ps-note ps-note-warn" data-testid="mcp-oauth-stepup-refused">
          {tr(stepUp.reason === "too_long" ? "mcp.oauth_stepup_too_long" : "mcp.oauth_stepup_invalid")}
        </p>
      )}
      {signedIn && status?.issuer && <div className="muted mono">{tr("mcp.oauth_as", { issuer: status.issuer })}</div>}
      {signedIn && status?.registration && (
        <div className="muted" data-testid="mcp-oauth-registration">
          {tr("mcp.oauth_reg_used", { path: tr(REG_LABEL[status.registration]) })}
        </div>
      )}
      <p className="muted" data-testid="mcp-oauth-relay-note">
        {tr(RELAY_NOTE[state])}
      </p>
      {(flow.unfinished || flow.cimdUnfinished) && !clientFor && (
        <div className="ps-note" data-testid="mcp-oauth-unfinished">
          <div>{flow.unfinished ? tr("mcp.oauth_unfinished_lead") : tr("mcp.oauth_dcr_note")}</div>
          <ul className="mcp-oauth-next">
            <li>{tr("mcp.oauth_unfinished_retry")}</li>
            {flow.cimdUnfinished && <li>{tr("mcp.oauth_unfinished_dcr")}</li>}
            <li>{tr("mcp.oauth_unfinished_trust")}</li>
          </ul>
          {flow.cimdUnfinished && (
            <button className="ghost mcp-btn" disabled={busy} onClick={() => void flow.signIn({ registration: "dcr" })}>
              {tr("mcp.oauth_btn_dcr")}
            </button>
          )}
        </div>
      )}
      {clientFor && <ClientIdForm issuer={clientFor} busy={busy} onSubmit={flow.submitClient} onCancel={() => flow.setClientFor(null)} />}
    </div>
  );
}

function ClientIdForm({
  issuer,
  busy,
  onSubmit,
  onCancel,
}: {
  issuer: string;
  busy: boolean;
  onSubmit: (id: string, secret: string) => Promise<void>;
  onCancel: () => void;
}) {
  const tr = useT();
  const [id, setId] = useState("");
  const [secret, setSecret] = useState("");
  return (
    <div className="mcp-oauth-client" data-testid="mcp-oauth-client">
      <div className="mcp-oauth-state">{tr("mcp.oauth_client_title")}</div>
      <p className="ps-note">{tr("mcp.oauth_client_hint", { issuer })}</p>
      <input
        className="cinput mono"
        aria-label={tr("mcp.oauth_client_id")}
        placeholder={tr("mcp.oauth_client_id")}
        value={id}
        onChange={(e) => setId(e.target.value)}
      />
      <input
        className="cinput"
        type="password"
        autoComplete="off"
        aria-label={tr("mcp.oauth_client_secret")}
        placeholder={tr("mcp.oauth_client_secret")}
        value={secret}
        onChange={(e) => setSecret(e.target.value)}
      />
      <div className="mcp-actions">
        <button className="primary mcp-btn" disabled={busy || id.trim() === ""} onClick={() => void onSubmit(id, secret)}>
          {tr("mcp.oauth_btn_client_save")}
        </button>
        <button className="ghost mcp-btn" onClick={onCancel}>
          {tr("common.cancel")}
        </button>
      </div>
    </div>
  );
}
