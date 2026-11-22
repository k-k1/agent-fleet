// The tenant's Web Push switch (#4750 decision 1): may a push carry a question's text? Off by
// default, because the push services (Apple, Google, Mozilla) relay the encrypted payload.
// tenant_admin of that tenant only. The setting is stored whether or not Web Push is switched on
// for the deployment, so an admin can prepare it first.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getTenantPushSettings, putTenantPushSettings } from "./tenantApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TenantPushView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [includeText, setIncludeText] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await getTenantPushSettings(slug);
      if (d && isOk(d)) setIncludeText(!!d.includeText);
    } catch {
      /* transient; the panel stays hidden */
    }
  }, [slug]);
  useEffect(() => {
    void load();
  }, [load]);

  if (includeText === null) return null;

  const save = async (on: boolean) => {
    setBusy(true);
    try {
      const res = await putTenantPushSettings(slug, { includeText: on });
      if (res && !isOk(res)) toast(errText(res.error));
      else if (res) setIncludeText(!!res.includeText);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel push-policy">
      <h4>{tr("tenant.push_title")}</h4>
      <p className="admin-hint">{tr("tenant.push_note")}</p>
      <label className="admin-check">
        <input type="checkbox" checked={includeText} disabled={busy} onChange={(e) => void save(e.target.checked)} /> {tr("tenant.push_include_text")}
      </label>
    </section>
  );
}
