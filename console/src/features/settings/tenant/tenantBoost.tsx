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
  // Set only when this month's hours were read (UTC, "2026-11"); members is then the whole
  // answer, absent meaning nobody ran boosted. Without month no hours are shown at all, so a
  // count that could not be read never reads as 0.
  month?: string;
  members?: BoostUsage[];
}

/** One member's boosted running hours this month, from the count the budget is enforced on. */
export interface BoostUsage {
  user_key: string;
  email?: string;
  used_hours: number;
  spent: boolean;
  over_hours: number;
}

/** Hours to one decimal; a sliver above zero is "<0.1" rather than a 0 that reads as none. */
export function fmtBoostHours(h: number): string {
  if (h > 0 && h < 0.05) return "<0.1";
  return String(Math.round(h * 10) / 10);
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
      {/* Shown with boost off too: hours run earlier this month are still this month's. */}
      {view.month && (view.enabled || !!view.members?.length) && <BoostUsageList month={view.month} budget={view.monthly_hours} members={view.members ?? []} />}
      {saved && (
        <span className="saved-note">
          <Icon name="check" /> {tr("admin.saved")}
        </span>
      )}
    </section>
  );
}

// BoostUsageList - who ran boosted this month and who went past the budget. A boosted workspace is
// never stopped at the budget (only new boosts and extensions are refused), so the hours can pass
// it, and this is where the tenant admin sees by how much (ADR 0139 decision 5).
function BoostUsageList({ month, budget, members }: { month: string; budget: number; members: BoostUsage[] }) {
  const tr = useT();
  const over = members.filter((m) => m.spent).length;
  return (
    <div className="boost-usage">
      <h5>{tr("tenant.boost_usage_title", { month })}</h5>
      {members.length === 0 ? (
        <p className="admin-hint">{tr("tenant.boost_usage_none")}</p>
      ) : (
        <>
          {budget > 0 && <p className={over ? "warn-text" : "admin-hint"}>{tr("tenant.boost_usage_over_count", { n: over })}</p>}
          <ul className="boost-usage-list">
            {members.map((m) => (
              <li key={m.user_key}>
                <span className="boost-usage-who">{m.email || m.user_key}</span>{" "}
                <span className="boost-usage-hours">{budget > 0 ? tr("tenant.boost_usage_of", { used: fmtBoostHours(m.used_hours), budget }) : tr("tenant.boost_usage_hours", { h: fmtBoostHours(m.used_hours) })}</span>
                <BoostOverNote u={m} />
              </li>
            ))}
          </ul>
          {budget > 0 && <p className="admin-hint">{tr("tenant.boost_usage_note")}</p>}
        </>
      )}
    </div>
  );
}

function BoostOverNote({ u }: { u: BoostUsage }) {
  const tr = useT();
  if (!u.spent) return null;
  return <span className="warn-text"> {u.over_hours > 0 ? tr("tenant.boost_usage_over", { h: fmtBoostHours(u.over_hours) }) : tr("tenant.boost_usage_spent")}</span>;
}

// MemberBoostLine - the member detail's line for the same count, directly under the cost card. It is
// its own card because the cost card is dollars over a chosen period and these are hours in the
// current month (ADR 0048 decision 2), and because it must show on a deployment with no bill too.
// It reads the tenant's Boost answer, which carries every member's hours, so there is one count and
// no second endpoint that could drift from it.
export function MemberBoostLine({ slug, userKey }: { slug: string; userKey: string }) {
  const tr = useT();
  const [view, setView] = useState<BoostPolicy | null>(null);
  useEffect(() => {
    let live = true;
    getTenantBoost(slug)
      .then((d) => {
        if (live && isOk(d)) setView(d);
      })
      .catch(() => {
        /* the line stays absent: no hours rather than a wrong 0 */
      });
    return () => {
      live = false;
    };
  }, [slug]);
  if (!view?.editable || !view.month) return null;
  const u = view.members?.find((m) => m.user_key === userKey);
  if (!view.enabled && !u) return null;
  const used = fmtBoostHours(u?.used_hours ?? 0);
  return (
    <section className="admin-panel member-boost">
      <h4>{tr("tenant.boost_member_title")}</h4>
      <p>
        {view.monthly_hours > 0
          ? tr("tenant.boost_member_line", { used, budget: view.monthly_hours, month: view.month })
          : tr("tenant.boost_member_line_nobudget", { used, month: view.month })}
        {u && <BoostOverNote u={u} />}
      </p>
    </section>
  );
}
