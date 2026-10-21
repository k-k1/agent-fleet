import { useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { useConfirm } from "../../../ui/ConfirmProvider.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";

export function TenantMCPOAuthView({ slug }: { slug: string }) {
  const tr = useT();
  const confirm = useConfirm();
  const toast = useToast();
  const [view, setView] = useState<{ enabled: boolean; served: boolean } | null>(null);
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let current = true;
    api(`api/admin/tenants/${encodeURIComponent(slug)}/mcp-oauth`).then((res) => {
      if (!current) return;
      if (res?.error) {
        setError(errText(res.error));
        return;
      }
      setView(res);
      setEnabled(res.enabled);
    }).catch(() => {
      if (current) setError(tr("tenant.mcp_oauth_load_failed"));
    });
    return () => { current = false; };
  }, [slug, tr]);
  const save = async () => {
    if (!enabled) {
      const ok = await confirm({
        title: tr("tenant.mcp_oauth_disable_title"),
        body: tr("tenant.mcp_oauth_disable_body"),
        confirmLabel: tr("common.save"),
        danger: true,
      });
      if (!ok) return;
    }
    setBusy(true);
    try {
      const res = await apiJSON(`api/admin/tenants/${encodeURIComponent(slug)}/mcp-oauth`, "PUT", { enabled });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setView(res);
      setEnabled(res.enabled);
      toast(tr("admin.saved"));
    } catch {
      toast(tr("tenant.mcp_oauth_save_failed"));
    } finally {
      setBusy(false);
    }
  };
  if (error) return <p role="alert" className="muted pad">{error}</p>;
  if (!view || !view.served) return null;
  return (
    <section className="admin-panel">
      <div className="admin-fgroup">
        <h4>{tr("tenant.mcp_oauth_title")}</h4>
        <label className="admin-check">
          <input type="checkbox" checked={enabled} disabled={busy} onChange={(e) => setEnabled(e.target.checked)} />
          <span>{tr("tenant.mcp_oauth_allow")}</span>
        </label>
        <p className="admin-hint">{tr("tenant.mcp_oauth_hint")}</p>
        <div className="le-actions">
          <button className="primary" disabled={busy || enabled === view.enabled} onClick={save}>
            {tr("common.save")}
          </button>
        </div>
      </div>
    </section>
  );
}
