// The deployment switch of the development OIDC IdP (control-plane/dev_idp_admin.go, ADR 0116).
//
// On unless a super admin turns it off here or the operator locks it with AF_DEV_IDP=0. The
// server's answer is the only source of what is shown: whether it is served, whether the
// environment forbids it, and why it cannot be on without PUBLIC_BASE_URL. Turning it off
// keeps the workspaces' opt-ins, which is why the count is on screen.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getDevIdp, putDevIdp } from "./adminApi.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export interface DevIdpStatus {
  enabled: boolean;
  setting: boolean;
  locked: boolean;
  missing_base_url: boolean;
  missing_preview_domain: boolean;
  issuer: string;
  clients: number;
}

export function DevIdpAdminView() {
  const tr = useT();
  const [data, setData] = useState<DevIdpStatus | null>(null);
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  // Resolves true only when the server's current state was read and is now on screen.
  const load = useCallback(async (): Promise<boolean> => {
    try {
      const d = await getDevIdp();
      if (!isOk(d)) {
        setErr(d ? errText(d.error) : tr("admin.load_error"));
        return false;
      }
      setErr("");
      setData(d);
      return true;
    } catch {
      setErr(tr("admin.load_error"));
      return false;
    }
  }, [tr]);
  useEffect(() => {
    load();
  }, [load]);

  const toggle = async (enabled: boolean) => {
    setBusy(true);
    try {
      const d = await putDevIdp(enabled);
      if (!isOk(d)) {
        if (d) setErr(errText(d.error));
        return;
      }
      setErr("");
      setData(d);
    } catch {
      // A lost response may still have applied the change: say so, then show the real state.
      if (await load()) {
        setErr(tr("admin.devidp_save_error"));
      } else {
        // Neither the change nor the state is known: do not leave the old value on screen as
        // if it were current.
        setData(null);
        setErr(tr("admin.devidp_unknown"));
      }
    } finally {
      setBusy(false);
    }
  };

  // The checkbox shows the admin's choice, not the effective state: with no PUBLIC_BASE_URL
  // the choice can stand while nothing is served, and the reason line says so.
  const disabled = busy || !data || data.locked || data.missing_base_url;
  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <div className="usage-toolbar">
          <span>{tr("admin.devidp_title")}</span>
          <button type="button" className="ghost" title={tr("admin.refresh")} onClick={load}>
            <Icon name="refresh" />
          </button>
        </div>
        <p className="muted">{tr("admin.devidp_note")}</p>
        <p className="form-err">{tr("admin.devidp_warn_prod")}</p>
        <label className="brand-field">
          <input
            type="checkbox"
            id="devidp-enabled"
            checked={!!data && data.setting && !data.locked && !data.missing_base_url}
            disabled={disabled}
            onChange={(e) => toggle(e.target.checked)}
          />{" "}
          {tr("admin.devidp_label")}
        </label>
        {data?.locked && <p className="muted">{tr("admin.devidp_locked")}</p>}
        {data && !data.locked && data.missing_base_url && (
          <p className="muted">{tr("admin.devidp_no_base_url")}</p>
        )}
        {data && !data.locked && data.missing_preview_domain && (
          <p className="muted">{tr("admin.devidp_no_preview_domain")}</p>
        )}
        {data?.enabled && data.issuer && (
          <p className="muted">
            {tr("admin.devidp_issuer")}: <span className="mono">{data.issuer}</span>
          </p>
        )}
        {data && <p className="muted">{tr("admin.devidp_clients", { n: data.clients })}</p>}
        {err && <p className="form-err">{err}</p>}
      </section>
    </div>
  );
}
