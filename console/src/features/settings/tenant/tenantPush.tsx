// The tenant's Web Push switch (#4750 decision 1): may a push carry a question's text? Off by
// default, because the push services (Apple, Google, Mozilla) relay the encrypted payload.
// tenant_admin of that tenant only. The setting is stored whether or not Web Push is switched on
// for the deployment, so an admin can prepare it first.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getTenantPushSettings, putTenantPushSettings } from "./tenantApi.ts";
import { usePushKey } from "../../push/usePushKey.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TenantPushView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [includeText, setIncludeText] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  // Only a deployment with Web Push shows the switch; the setting itself is stored either way.
  const pushOn = !!usePushKey();

  // Read once Web Push is known to be on, and again after a transient failure: the same gateway
  // outage that hid the key would otherwise leave the switch absent until the screen is reopened.
  const load = useCallback(async (): Promise<boolean> => {
    try {
      const d = await getTenantPushSettings(slug);
      if (d && isOk(d)) setIncludeText(!!d.includeText);
      return true;
    } catch {
      return false;
    }
  }, [slug]);
  useEffect(() => {
    if (!pushOn) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const go = () =>
      void load().then((ok) => {
        if (!ok && alive) timer = setTimeout(go, 15_000);
      });
    go();
    return () => {
      alive = false;
      if (timer) clearTimeout(timer);
    };
  }, [pushOn, load]);

  if (includeText === null || !pushOn) return null;

  const save = async (on: boolean) => {
    setBusy(true);
    try {
      const res = await putTenantPushSettings(slug, { includeText: on });
      if (res && !isOk(res)) toast(errText(res.error));
      else if (res) setIncludeText(!!res.includeText);
    } catch (e) {
      toast(tr("push.failed", { msg: e instanceof Error ? e.message : String(e) }));
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
