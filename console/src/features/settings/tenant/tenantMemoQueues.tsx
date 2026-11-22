// The tenant-wide switch for memo auto-run (ADR 0137 §3, decision 14 of the contract slice).
//
// Off by default. With it off a member cannot create auto-run settings (the Control Plane answers
// 409 memo_queues_disabled); existing settings stay and can still be read and removed. The
// server owns the answer; this screen shows what it stores and never infers it.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getTenantMemoQueues, putTenantMemoQueues } from "./tenantApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TenantMemoQueuesView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await getTenantMemoQueues(slug);
      if (isOk(d) && typeof d.enabled === "boolean") {
        setEnabled(d.enabled);
        setFailed(false);
        return;
      }
    } catch {
      /* falls through to the failed note */
    }
    setFailed(true);
  }, [slug]);
  useEffect(() => {
    load();
  }, [load]);

  const put = async (next: boolean) => {
    if (next === enabled) return;
    setBusy(true);
    try {
      const res = await putTenantMemoQueues(slug, next);
      if (res && !isOk(res)) {
        toast(errText(res.error));
        return;
      }
      setEnabled(isOk(res) ? (res.enabled ?? next) : next);
    } catch {
      toast(tr("tenant.memo_queues_save_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel memo-queues">
      <h4>{tr("tenant.memo_queues_title")}</h4>
      <p className="admin-hint">{tr("tenant.memo_queues_note")}</p>
      {enabled === null ? (
        <p className={failed ? "admin-hint warn" : "muted"}>
          {failed ? tr("tenant.memo_queues_load_failed") : tr("common.loading")}
        </p>
      ) : (
        <>
          <span className="seg sm" role="radiogroup" aria-label={tr("tenant.memo_queues_title")}>
            <button
              type="button"
              role="radio"
              aria-checked={enabled}
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(true)}
            >
              {tr("tenant.memo_queues_on")}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={!enabled}
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(false)}
            >
              {tr("tenant.memo_queues_off")}
            </button>
          </span>
          <p className="admin-hint">{enabled ? tr("tenant.memo_queues_on_hint") : tr("tenant.memo_queues_off_hint")}</p>
        </>
      )}
    </section>
  );
}
