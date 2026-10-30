// The tenant-wide switch for Agent Fleet memory used from outside Agent Fleet through the CP's
// /mcp endpoint (#2318, ADR 0108).
//
// It only gates what each member may then opt into: with it off, the memory tools are neither
// listed nor callable for anyone in the tenant whatever their own setting says, and with it on
// every member's setting still starts off. The server owns the answer; this screen shows what it
// stores and never infers it.
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errText } from "../../../core/api/client.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TenantExternalMemoryView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/external-memory`;

  const load = useCallback(async () => {
    try {
      const d = await api(path);
      if (d && !d.error && typeof d.enabled === "boolean") {
        setEnabled(d.enabled);
        setFailed(false);
        return;
      }
    } catch {
      /* falls through to the failed note */
    }
    setFailed(true);
  }, [path]);
  useEffect(() => {
    load();
  }, [load]);

  const put = async (next: boolean) => {
    if (next === enabled) return;
    setBusy(true);
    try {
      const res = await apiJSON(path, "PUT", { enabled: next });
      if (res?.error) {
        toast(errText(res.error));
        return;
      }
      setEnabled(res?.enabled ?? next);
    } catch {
      toast(tr("tenant.ext_memory_save_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel ext-memory">
      <h4>{tr("tenant.ext_memory_title")}</h4>
      <p className="admin-hint">{tr("tenant.ext_memory_note")}</p>
      {enabled === null ? (
        <p className={failed ? "admin-hint warn" : "muted"}>
          {failed ? tr("tenant.ext_memory_load_failed") : tr("common.loading")}
        </p>
      ) : (
        <>
          <span className="seg sm" role="radiogroup" aria-label={tr("tenant.ext_memory_title")}>
            <button
              type="button"
              role="radio"
              aria-checked={enabled}
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(true)}
            >
              {tr("tenant.ext_memory_allow")}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={!enabled}
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(false)}
            >
              {tr("tenant.ext_memory_block")}
            </button>
          </span>
          <p className="admin-hint">{enabled ? tr("tenant.ext_memory_on_hint") : tr("tenant.ext_memory_off_hint")}</p>
        </>
      )}
    </section>
  );
}
