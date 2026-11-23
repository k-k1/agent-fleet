// The tenant-wide switch for member-added watch sources (ADR 0133 §8, #3631).
//
// Off by default. With it off a member cannot mark an MCP server as a watch source and every
// Workspace Agent in the tenant runs none; with it on, members still choose per server. The
// server owns the answer; this screen shows what it stores and never infers it.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { isOk } from "../api.ts";
import { getTenantWatchSources, putTenantWatchSources } from "./tenantApi.ts";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";

export function TenantWatchSourcesView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [failed, setFailed] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const d = await getTenantWatchSources(slug);
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
      const res = await putTenantWatchSources(slug, next);
      if (res && !isOk(res)) {
        toast(errText(res.error));
        return;
      }
      setEnabled(isOk(res) ? (res.enabled ?? next) : next);
    } catch {
      toast(tr("tenant.watch_src_save_failed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel watch-src">
      <h4>{tr("tenant.watch_src_title")}</h4>
      <p className="admin-hint">{tr("tenant.watch_src_note")}</p>
      {enabled === null ? (
        <p className={failed ? "admin-hint admin-hint--warn" : "muted"}>
          {failed ? tr("tenant.watch_src_load_failed") : tr("common.loading")}
        </p>
      ) : (
        <>
          <span className="seg sm" role="radiogroup" aria-label={tr("tenant.watch_src_title")}>
            <button
              type="button"
              role="radio"
              aria-checked={enabled}
              className={"seg-btn" + (enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(true)}
            >
              {tr("tenant.watch_src_on")}
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={!enabled}
              className={"seg-btn" + (!enabled ? " active" : "")}
              disabled={busy}
              onClick={() => put(false)}
            >
              {tr("tenant.watch_src_off")}
            </button>
          </span>
          <p className="admin-hint">{enabled ? tr("tenant.watch_src_on_hint") : tr("tenant.watch_src_off_hint")}</p>
        </>
      )}
    </section>
  );
}
