// The tenant's Boost policy (ADR 0139 decision 5): on/off, the highest rung a boost may reach and
// a monthly budget of boosted running hours per member. tenant_admin of that tenant only (the PUT
// is gated by tenantAdminFor), like the default machine class beside it.
//
// The rung choices are read from the deployment's own sizing profile, not typed: a boost can only
// reach a rung the deployment declares, and the CP refuses any other value.
import { useCallback, useEffect, useState } from "react";
import { errText } from "../../../core/api/client.ts";
import { getWorkspaceSizing, isOk } from "../api.ts";
import { getTenantBoost, putTenantBoost } from "./tenantApi.ts";
import { Icon } from "../../../ui/Icon.tsx";
import { useToast } from "../../../ui/ToastProvider.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { fmtGbHint, WS_SIZING_FALLBACK } from "../parts/adminShared.ts";
import type { WsSizing } from "../parts/adminShared.ts";

export interface BoostPolicy {
  enabled: boolean;
  max_mem_mib: number;
  monthly_hours: number;
  editable: boolean;
  budget_counted: boolean;
}

/** Every distinct rung memory the deployment declares, ascending. */
export function declaredRungs(sizing: WsSizing): number[] {
  const all = (sizing.slot_classes?.length ? sizing.slot_classes.flatMap((c) => c.slots) : (sizing.slots ?? [])).map((s) => s.mem_mib);
  return [...new Set(all)].sort((a, b) => a - b);
}

export function TenantBoostView({ slug }: { slug: string }) {
  const tr = useT();
  const toast = useToast();
  const [view, setView] = useState<BoostPolicy | null>(null);
  const [rungs, setRungs] = useState<number[]>([]);
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);

  const load = useCallback(async () => {
    try {
      const [d, sz] = await Promise.all([getTenantBoost(slug), getWorkspaceSizing()]);
      if (isOk(d)) setView(d);
      setRungs(declaredRungs({ ...WS_SIZING_FALLBACK, ...(isOk(sz) ? sz : {}) }));
    } catch {
      /* transient; the panel keeps its last values */
    }
  }, [slug]);
  useEffect(() => {
    void load();
  }, [load]);

  // Absent on a runtime with no rungs: there is nothing to boost between.
  if (!view || !view.editable) return null;

  const save = async (next: Partial<BoostPolicy>) => {
    const want = { enabled: view.enabled, max_mem_mib: view.max_mem_mib, monthly_hours: view.monthly_hours, ...next };
    setBusy(true);
    try {
      const res = await putTenantBoost(slug, want);
      if (res && !isOk(res)) {
        toast(errText(res.error));
        return;
      }
      setView(res);
      setSaved(true);
      setTimeout(() => setSaved(false), 2000);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="admin-panel boost-policy">
      <h4>{tr("tenant.boost_title")}</h4>
      <p className="admin-hint">{tr("tenant.boost_note")}</p>
      <label className="admin-check">
        <input type="checkbox" checked={view.enabled} disabled={busy} onChange={(e) => save({ enabled: e.target.checked })} />{" "}
        {tr("tenant.boost_enabled")}
      </label>
      {view.enabled && (
        <>
          <div className="le-presets">
            <span className="admin-hint">{tr("tenant.boost_max")}</span>
            <button className={view.max_mem_mib === 0 ? "chip chip--on" : "chip"} aria-pressed={view.max_mem_mib === 0} disabled={busy} onClick={() => save({ max_mem_mib: 0 })}>
              {tr("tenant.boost_max_top")}
            </button>
            {rungs.map((r) => (
              <button key={r} className={view.max_mem_mib === r ? "chip chip--on" : "chip"} aria-pressed={view.max_mem_mib === r} disabled={busy} onClick={() => save({ max_mem_mib: r })}>
                {fmtGbHint(r)}
              </button>
            ))}
          </div>
          <p className="admin-hint boost-current">
            {tr("tenant.boost_current", { size: view.max_mem_mib === 0 ? tr("tenant.boost_max_top") : fmtGbHint(view.max_mem_mib) })}
          </p>
          <label className="admin-field">
            <span className="admin-hint">{tr("tenant.boost_hours")}</span>
            <input
              type="number"
              min={0}
              defaultValue={view.monthly_hours}
              disabled={busy}
              onBlur={(e) => {
                const n = Math.max(0, Math.floor(+e.target.value || 0));
                if (n !== view.monthly_hours) void save({ monthly_hours: n });
              }}
            />
          </label>
          {!view.budget_counted && <p className="admin-hint">{tr("tenant.boost_uncounted")}</p>}
        </>
      )}
      {saved && (
        <span className="saved-note">
          <Icon name="check" /> {tr("admin.saved")}
        </span>
      )}
    </section>
  );
}
