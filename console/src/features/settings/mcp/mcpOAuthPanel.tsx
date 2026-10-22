import { useCallback, useEffect, useRef, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { fmtDateTime, DATETIME_FULL } from "../../../lib/intl.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { McpServer } from "./mcpWire.ts";
import { disconnectOAuth, fetchOAuthStatus, saveClientId, startSignIn } from "./mcpOAuthApi.ts";
import type { OAuthRegistration, OAuthReturn, OAuthState, OAuthStatus } from "./mcpOAuth.ts";

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

  const reload = useCallback(async () => {
    if (!active) return;
    const mine = ++seq.current;
    const r = await fetchOAuthStatus(s.id);
    if (mine !== seq.current) return; // a newer read is in flight
    if ("error" in r) {
      setStatusErr(errText(r.error as any));
      return;
    }
    setStatusErr("");
    setStatus(r);
  }, [active, s.id]);
  useEffect(() => {
    void reload();
  }, [reload]);

  // Navigates away on success: the authorization server owns the next screen and the Control
  // Plane's callback brings the browser back.
  const signIn = useCallback(
    async (opts: { registration?: "dcr" | "cimd" | "preregistered" } = {}) => {
      setBusy(true);
      try {
        const r = await startSignIn(s.id, opts);
        if ("error" in r) {
          if (r.error.code === "client_id_needed" && r.error.issuer) {
            setClientFor(r.error.issuer);
            return;
          }
          toast(tr("mcp.oauth_signin_failed", { msg: errText(r.error as any) }));
          return;
        }
        window.location.assign(r.authorizeUrl);
      } finally {
        setBusy(false);
      }
    },
    [s.id, toast, tr],
  );

  const submitClient = useCallback(
    async (clientId: string, secret: string) => {
      if (!clientFor) return;
      setBusy(true);
      try {
        const r = await saveClientId(s.id, clientFor, clientId, secret);
        if (r.error) {
          toast(tr("mcp.save_failed", { msg: errText(r.error as any) }));
          return;
        }
        setClientFor(null);
      } finally {
        setBusy(false);
      }
      await signIn(); // the stored client id is first in the registration order
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
    (status?.pendingRegistration === "cimd" || (failedReturn?.server === s.id && failedReturn.registration === "cimd"));

  return { active, status, statusErr, busy, clientFor, setClientFor, signIn, submitClient, disconnect, reload, cimdUnfinished };
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

// OAuthPanel is the sign-in block of an OAuth row: state, what was granted, and the buttons.
export function OAuthPanel({ flow }: { flow: OAuthFlow }) {
  const tr = useT();
  const { status, statusErr, busy, clientFor } = flow;
  if (!flow.active) return null;
  const state = status?.state ?? "not_signed_in";
  const signedIn = state === "connected" || state === "sign_in_again" || state === "needs_more_permission";
  const again = state === "sign_in_again" || state === "needs_more_permission";
  return (
    <div className={"mcp-oauth mcp-oauth-" + state} data-testid="mcp-oauth-panel" data-state={state}>
      <div className="mcp-oauth-head">
        <span className="mcp-oauth-state">{tr(STATE_LABEL[state])}</span>
        <span className="mcp-actions">
          <button className="primary mcp-btn" disabled={busy || !status} onClick={() => void flow.signIn()}>
            {again ? tr("mcp.oauth_btn_signin_again") : tr("mcp.oauth_btn_signin")}
          </button>
          {signedIn && (
            <button className="ghost danger mcp-btn" disabled={busy} onClick={() => void flow.disconnect()}>
              {tr("mcp.oauth_btn_disconnect")}
            </button>
          )}
        </span>
      </div>
      {statusErr && <p className="ps-note ps-note-warn">{tr("mcp.oauth_status_failed", { msg: statusErr })}</p>}
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
      {signedIn && status?.issuer && <div className="muted mono">{tr("mcp.oauth_as", { issuer: status.issuer })}</div>}
      {signedIn && status?.registration && (
        <div className="muted" data-testid="mcp-oauth-registration">
          {tr("mcp.oauth_reg_used", { path: tr(REG_LABEL[status.registration]) })}
        </div>
      )}
      {flow.cimdUnfinished && !clientFor && (
        <div className="ps-note">
          <div>{tr("mcp.oauth_dcr_note")}</div>
          <button className="ghost mcp-btn" disabled={busy} onClick={() => void flow.signIn({ registration: "dcr" })}>
            {tr("mcp.oauth_btn_dcr")}
          </button>
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
