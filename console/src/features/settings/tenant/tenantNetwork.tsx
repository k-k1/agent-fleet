// The tenant's source-address restriction (docs/log/66, ADR 0047).
//
// The one thing this screen protects: it is the server that refuses to save a list not
// containing the caller's own address, rather than the UI merely displaying that address. If it
// only displayed it, on a deployment that has not declared its proxy the visible address is the
// ALB's private one, which someone would register as "my IP" and so let everyone through while
// believing they had restricted access (decision 4). editable / reason therefore mirror the
// server's answer and state why the control cannot be used.
//
// The screen also states that this is not a network defence: the request reaches the CP through
// the ALB and is rejected after the session has been validated. It only covers "someone holding
// valid credentials touches data from a place that is not allowed" (decision 1).
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

interface NetworkView {
  allowed_cidrs?: string;
  your_ip?: string;
  proxy_hops?: number;
  editable?: boolean;
  reason?: string;
}

export function TenantNetworkView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [view, setView] = useState<NetworkView | null>(null);
  const [cidrs, setCidrs] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await api(`api/admin/tenants/${encodeURIComponent(slug)}/network`);
      if (d && !d.error) {
        setView(d);
        setCidrs(d.allowed_cidrs || "");
      }
    } catch {
      /* transient; the panel simply stays on its last values */
    }
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  const save = async () => {
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/network`, "PUT", {
        allowed_cidrs: cidrs,
      });
      if (res?.error) {
        // Show the server's wording verbatim: a lockout refusal is only actionable once it
        // says "your address is 203.0.113.9 and it is not in this list".
        toast(errText(res.error));
        return;
      }
      // Write back the normalised result (192.0.2.7/24 is stored as 192.0.2.0/24).
      setCidrs(res?.allowed_cidrs || "");
      setSaved(true);
      setTimeout(() => setSaved(false), 1500);
      load();
    } finally {
      setBusy(false);
    }
  };

  if (!view) return <p className="muted pad">{tr("common.loading")}</p>;
  const on = (view.allowed_cidrs || "").trim() !== "";

  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>
          {tr("tenant.net_title")}
          <span className="af-note">{on ? tr("tenant.net_on") : tr("tenant.net_off")}</span>
        </h4>
        <div className="admin-fgrid">
          <label className="admin-fld wide">
            <span className="af-cap">{tr("tenant.net_allowed")}</span>
            <input
              type="text"
              placeholder="203.0.113.0/24, 198.51.100.7"
              value={cidrs}
              disabled={!view.editable}
              onChange={(e) => setCidrs(e.target.value)}
            />
            <span className="af-unit">{tr("tenant.net_allowed_unit")}</span>
          </label>
          <div className="admin-fld">
            <span className="af-cap">{tr("tenant.net_your_ip")}</span>
            <span className={"af-val" + (view.your_ip ? "" : " unset")}>
              {view.your_ip || tr("tenant.net_ip_unknown")}
            </span>
            <span className="af-unit">{tr("tenant.net_your_ip_unit")}</span>
          </div>
        </div>
        {view.reason === "proxy_not_configured" && (
          <p className="admin-hint warn">{tr("tenant.net_proxy_not_configured")}</p>
        )}
        {view.reason === "client_ip_unknown" && <p className="admin-hint warn">{tr("tenant.net_ip_unknown_hint")}</p>}
        <p className="admin-hint">{tr("tenant.net_scope_hint")}</p>
        <p className="admin-hint">{tr("tenant.net_exempt_hint")}</p>
        <p className="admin-hint">{tr("tenant.net_layers_hint")}</p>
        <div className="le-actions">
          <button className="primary" disabled={busy || !view.editable} onClick={save}>
            {tr("common.save")}
          </button>
          {saved && (
            <span className="saved-note">
              <Icon name="check" /> {tr("admin.saved")}
            </span>
          )}
        </div>
      </div>
    </section>
  );
}
