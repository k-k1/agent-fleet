// Provided routes on Tenant settings > Model access (ADR 0129 D3, D4; ADR 0138 M2 P10): the
// deployment's grant to this tenant with its monthly ceiling (read-only here, the super_admin's),
// the tenant_admin's "every member" / "granted members only" and default monthly budget, and each
// member's grant and budget override, with this month's spend.
//
// Drawn only where the CP serves provided routes: its plain 404 draws nothing. Any other failed
// read draws a "could not be read" line and no editor.
//
// No PUT here carries a revision, so each save re-reads the stored values first and refuses when
// they moved since the page loaded; a save racing in between is not caught (#4367).
import { useCallback, useEffect, useState } from "react";
import { api, apiJSON, errDetail, errText } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import { useReportDirty } from "../parts/dirtyScope.tsx";
import { useShowView } from "../parts/tabbedViews.tsx";
import { fmtDateTime } from "../../../lib/intl.ts";
import {
  isOff,
  parseProvidedTenant,
  parseUsd,
  resetOf,
  usd,
  usdInput,
  type Load,
  type GrantInput,
  type MemberInput,
  type PolicyInput,
  type ProvidedMember,
  type ProvidedTenantView,
} from "./providedRoutes.ts";

/** useProvided reads one provided-routes path. null while the first read is in flight. */
export function useProvided<T>(path: string, parse: (d: unknown) => T | null) {
  const tr = useT();
  const [load, setLoad] = useState<Load<T> | null>(null);
  const read = useCallback(async (): Promise<Load<T>> => {
    try {
      const d = await api(path);
      if (isOff(d)) return { state: "off" };
      const v = parse(d);
      if (v !== null) return { state: "ok", value: v };
      return { state: "failed", reason: d?.error ? errText(d.error) : null };
    } catch {
      return { state: "failed", reason: tr("admin.prov_load_failed") };
    }
  }, [path, parse, tr]);
  const reload = useCallback(async () => setLoad(await read()), [read]);
  useEffect(() => {
    reload();
  }, [reload]);
  /** peek reads the stored value now without replacing the drawn one: the pre-save check. */
  const peek = useCallback(async () => {
    const l = await read();
    return l.state === "ok" ? l.value : null;
  }, [read]);
  return { load, reload, peek };
}

/** useGuardedSave runs one save: re-check that what was loaded is still what is stored, PUT,
 *  and keep a refusal on the page with the edits. onSaved gets the CP's answer (its warnings). */
export function useGuardedSave(onSaved: (res: { warnings?: unknown }) => void) {
  const tr = useT();
  const [busy, setBusy] = useState(false);
  const [refusal, setRefusal] = useState<string | null>(null);
  const submit = async (unchanged: () => Promise<boolean | null>, put: () => Promise<unknown>) => {
    setBusy(true);
    setRefusal(null);
    try {
      const same = await unchanged();
      if (same === null) {
        setRefusal(tr("admin.prov_load_failed"));
        return;
      }
      if (!same) {
        setRefusal(tr("admin.mpol_stale"));
        return;
      }
      const res = (await put()) as { error?: Parameters<typeof errDetail>[0]; warnings?: unknown } | null | undefined;
      if (res?.error) {
        setRefusal(errDetail(res.error));
        return;
      }
      onSaved(res ?? {});
    } catch {
      setRefusal(tr("admin.mpol_save_failed"));
    } finally {
      setBusy(false);
    }
  };
  return { busy, refusal, setRefusal, submit };
}

/** Refusal: a save's refusal, beside its button. */
export function Refusal({ text }: { text: string | null }) {
  const tr = useT();
  if (!text) return null;
  return (
    <p className="admin-hint warn" role="alert">
      {tr("admin.mpol_refused", { reason: text })}
    </p>
  );
}

/** Warnings: the CP's notes on a saved amount that admits a call only with a small output cap. */
export function Warnings({ list }: { list: string[] }) {
  if (list.length === 0) return null;
  return (
    <ul className="admin-hint warn prov-warnings" role="status">
      {list.map((w) => (
        <li key={w}>{w}</li>
      ))}
    </ul>
  );
}

export const warningsOf = (res: { warnings?: unknown }) =>
  Array.isArray(res.warnings) ? res.warnings.filter((w): w is string => typeof w === "string") : [];

export const approx = (micro: number) => "≈ " + usd(micro);

/** ProvidedSpend: the tenant's grant, ceiling and this month's spend, read-only. */
export function ProvidedSpend({ view }: { view: ProvidedTenantView }) {
  const tr = useT();
  return (
    <>
      <p className="admin-hint" data-testid="prov-grant">
        {view.granted
          ? tr("admin.prov_granted", { ceiling: view.ceiling_micro === null ? "—" : approx(view.ceiling_micro) })
          : tr("admin.prov_not_granted")}
      </p>
      <p className="admin-hint">
        {tr("admin.prov_month", {
          month: view.month,
          spent: approx(view.spent_micro),
          reserved: approx(view.reserved_micro),
        })}
      </p>
      <Reservations view={view} />
    </>
  );
}

/** Reservations: what one call holds per model, folded. */
function Reservations({ view }: { view: ProvidedTenantView }) {
  const tr = useT();
  return (
    <>
      {view.reservations.length > 0 && (
        <details className="prov-reservations">
          <summary>{tr("admin.prov_reservations", { floor: approx(view.floor_micro) })}</summary>
          <table className="admin-table">
            <thead>
              <tr>
                <th>{tr("admin.prov_model")}</th>
                <th>{tr("admin.prov_res_min")}</th>
                <th>{tr("admin.prov_res_max")}</th>
              </tr>
            </thead>
            <tbody>
              {view.reservations.map((r) => (
                <tr key={r.model}>
                  <td>
                    <code>{r.model}</code>
                  </td>
                  <td>{approx(r.min_micro)}</td>
                  <td>{approx(r.max_output_micro)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
    </>
  );
}

/** Usage: an amount against its limit as text and a bar. The bar counts what is held for calls in
 *  flight too, the way admission does (ADR 0129 D4); without a limit only the text is shown. */
export function Usage({ used, of, spent }: { used: number; of: number | null; spent: number }) {
  const tr = useT();
  if (of === null || of <= 0) return <>{approx(spent)}</>;
  return (
    <span className="prov-usage">
      {tr("tenant.prov_use_of", { used: approx(spent), of: approx(of) })}
      <meter
        min={0}
        max={of}
        value={Math.min(used, of)}
        // The same amount the text says; the bar's own value also counts what is held.
        aria-label={tr("tenant.prov_use_of", { used: approx(spent), of: approx(of) })}
      />
    </span>
  );
}

/** TenantProvidedSummary: whether the tenant may use the route, its month against the pool, and
 *  when the month resets. Where it is not granted the budgets below stay editable: they apply from
 *  the moment it is (#4380 decision 9). */
function TenantProvidedSummary({ view }: { view: ProvidedTenantView }) {
  const tr = useT();
  const reset = resetOf(view.month);
  const pool = view.granted ? view.ceiling_micro : null;
  return (
    <div className="prov-summary" data-testid="prov-grant">
      {view.granted ? (
        <p className="mpol-ok" data-state="granted">
          {tr("tenant.prov_status_on")}
          {view.ceiling_micro !== null && (
            <span className="muted"> {tr("tenant.prov_pool", { pool: approx(view.ceiling_micro) })}</span>
          )}
        </p>
      ) : (
        <div className="admin-hint warn prov-banner" data-state="not-granted">
          <p>{tr("tenant.prov_status_off")}</p>
          <p>{tr("tenant.prov_status_off_hint")}</p>
        </div>
      )}
      <p className="admin-hint">
        {reset
          ? tr("tenant.prov_month", { month: view.month, reset: fmtDateTime(reset) })
          : tr("tenant.prov_month_noreset", { month: view.month })}
      </p>
      {pool !== null ? (
        <p className="prov-tenant-use">
          <Usage used={view.spent_micro + view.reserved_micro} of={pool} spent={view.spent_micro} />
        </p>
      ) : (
        view.spent_micro > 0 && (
          <p className="admin-hint">{tr("tenant.prov_spent", { spent: approx(view.spent_micro) })}</p>
        )
      )}
      {view.reserved_micro > 0 && (
        <p className="admin-hint">{tr("tenant.prov_held", { held: approx(view.reserved_micro) })}</p>
      )}
      <Reservations view={view} />
    </div>
  );
}

/** GrantEditor: the super_admin's grant to this tenant and the tenant's monthly pool. A tenant
 *  administrator never gets it: the CP refuses their grant write, and the page does not draw it. */
function GrantEditor({
  path,
  view,
  peek,
  onSaved,
}: {
  path: string;
  view: ProvidedTenantView;
  peek: () => Promise<ProvidedTenantView | null>;
  onSaved: (warnings: string[]) => void;
}) {
  const tr = useT();
  const [granted, setGranted] = useState(view.granted);
  const [ceiling, setCeiling] = useState(usdInput(view.ceiling_micro));
  const { busy, refusal, setRefusal, submit } = useGuardedSave((res) => onSaved(warningsOf(res)));
  const micro = parseUsd(ceiling);
  const dirty = granted !== view.granted || micro !== view.ceiling_micro;
  useReportDirty(dirty);
  const save = () => {
    if (micro === undefined) {
      setRefusal(tr("admin.prov_bad_amount"));
      return;
    }
    const body: GrantInput = { granted, ceiling_micro: micro };
    submit(
      async () => {
        const now = await peek();
        return now && now.granted === view.granted && now.ceiling_micro === view.ceiling_micro;
      },
      () => apiJSON(`${path}/grant`, "PUT", body),
    );
  };
  return (
    <div className="admin-fgroup prov-grant">
      <label className="admin-check">
        <input
          type="checkbox"
          checked={granted}
          disabled={busy}
          onChange={(e) => {
            setGranted(e.target.checked);
            setRefusal(null);
          }}
        />
        {tr("admin.prov_grant")}
      </label>
      <label className="admin-fld">
        <span className="af-cap">{tr("admin.prov_ceiling")}</span>
        <input
          type="text"
          inputMode="decimal"
          value={ceiling}
          disabled={busy}
          onChange={(e) => {
            setCeiling(e.target.value);
            setRefusal(null);
          }}
        />
      </label>
      <Refusal text={refusal} />
      <div className="admin-actions">
        <button type="button" className="btn primary" disabled={busy || !dirty} onClick={save}>
          {tr("admin.mpol_save")}
        </button>
      </div>
    </div>
  );
}

function PolicyEditor({
  path,
  view,
  peek,
  onSaved,
}: {
  path: string;
  view: ProvidedTenantView;
  peek: () => Promise<ProvidedTenantView | null>;
  onSaved: (warnings: string[]) => void;
}) {
  const tr = useT();
  const [membersOnly, setMembersOnly] = useState(view.members_only);
  const [budget, setBudget] = useState(usdInput(view.member_default_micro));
  const { busy, refusal, setRefusal, submit } = useGuardedSave((res) => onSaved(warningsOf(res)));
  const micro = parseUsd(budget);
  const dirty = membersOnly !== view.members_only || micro !== view.member_default_micro;
  useReportDirty(dirty);
  const save = () => {
    // The default budget has no "none": an empty field is zero, which is no use (D3).
    if (micro === undefined) {
      setRefusal(tr("admin.prov_bad_amount"));
      return;
    }
    const body: PolicyInput = { members_only: membersOnly, member_default_micro: micro ?? 0 };
    submit(
      async () => {
        const now = await peek();
        return now && now.members_only === view.members_only && now.member_default_micro === view.member_default_micro;
      },
      () => apiJSON(`${path}/policy`, "PUT", body),
    );
  };
  return (
    <div className="admin-fgroup prov-policy">
      <h5 className="admin-subhead">{tr("admin.prov_policy")}</h5>
      <span className="seg sm" role="radiogroup" aria-label={tr("admin.prov_policy")}>
        {[false, true].map((only) => (
          <button
            key={String(only)}
            type="button"
            role="radio"
            aria-checked={membersOnly === only}
            className={"seg-btn" + (membersOnly === only ? " active" : "")}
            disabled={busy}
            onClick={() => {
              setMembersOnly(only);
              setRefusal(null);
            }}
          >
            {tr(only ? "admin.prov_members_only" : "admin.prov_every_member")}
          </button>
        ))}
      </span>
      <label className="admin-fld">
        <span className="af-cap">{tr("admin.prov_default_budget")}</span>
        <input
          type="text"
          inputMode="decimal"
          value={budget}
          disabled={busy}
          onChange={(e) => {
            setBudget(e.target.value);
            setRefusal(null);
          }}
        />
      </label>
      {view.member_default_micro === 0 && <p className="admin-hint">{tr("tenant.prov_zero_default")}</p>}
      <Refusal text={refusal} />
      <div className="admin-actions">
        <button type="button" className="btn primary" disabled={busy || !dirty} onClick={save}>
          {tr("admin.mpol_save")}
        </button>
      </div>
    </div>
  );
}

function MemberRow({
  path,
  member,
  canUse,
  peek,
  onSaved,
}: {
  path: string;
  member: ProvidedMember;
  /** The member may use the route as far as the grants go: the tenant is granted it, and the
   *  member is everyone or granted under "granted members only". */
  canUse: boolean;
  peek: () => Promise<ProvidedTenantView | null>;
  onSaved: (warnings: string[]) => void;
}) {
  const tr = useT();
  const [granted, setGranted] = useState(member.granted);
  const [budget, setBudget] = useState(usdInput(member.budget_micro));
  const { busy, refusal, setRefusal, submit } = useGuardedSave((res) => onSaved(warningsOf(res)));
  const micro = parseUsd(budget);
  const dirty = granted !== member.granted || micro !== member.budget_micro;
  useReportDirty(dirty);
  const save = () => {
    if (micro === undefined) {
      setRefusal(tr("admin.prov_bad_amount"));
      return;
    }
    const body: MemberInput = { membership_id: member.membership_id, granted, budget_micro: micro };
    submit(
      async () => {
        const now = await peek();
        if (!now) return null;
        const m = now.members.find((x) => x.membership_id === member.membership_id);
        return !!m && m.granted === member.granted && m.budget_micro === member.budget_micro;
      },
      () => apiJSON(`${path}/members`, "PUT", body),
    );
  };
  return (
    <tr data-member={member.user_key}>
      <td className="prov-member-name">{member.user_key}</td>
      <td data-label={tr("admin.prov_col_granted")}>
        <input
          type="checkbox"
          aria-label={tr("admin.prov_member_granted", { user: member.user_key })}
          checked={granted}
          disabled={busy}
          onChange={(e) => {
            setGranted(e.target.checked);
            setRefusal(null);
          }}
        />
      </td>
      <td data-label={tr("admin.prov_col_budget")}>
        <input
          type="text"
          inputMode="decimal"
          aria-label={tr("admin.prov_member_budget", { user: member.user_key })}
          placeholder={tr("admin.prov_default")}
          value={budget}
          disabled={busy}
          onChange={(e) => {
            setBudget(e.target.value);
            setRefusal(null);
          }}
        />
      </td>
      <td data-label={tr("tenant.prov_col_use")}>
        {!canUse ? (
          // Not granted (the tenant, or the member under "granted members only"): no budget applies,
          // so none is shown as theirs. What they spent or still hold this month stays visible.
          member.spent_micro + member.reserved_micro > 0 ? (
            [
              ...(member.spent_micro > 0 ? [approx(member.spent_micro)] : []),
              ...(member.reserved_micro > 0 ? [tr("tenant.prov_held", { held: approx(member.reserved_micro) })] : []),
            ].join(" · ")
          ) : (
            "—"
          )
        ) : member.effective_micro === 0 ? (
          <span className="mpol-no">{tr("tenant.prov_zero_member")}</span>
        ) : (
          <Usage
            used={member.spent_micro + member.reserved_micro}
            of={member.effective_micro}
            spent={member.spent_micro}
          />
        )}
      </td>
      <td className="prov-member-save">
        <button type="button" className="btn xs" disabled={busy || !dirty} onClick={save}>
          {tr("admin.mpol_save")}
        </button>
        <Refusal text={refusal} />
      </td>
    </tr>
  );
}

/** TenantProvidedRoutesView is the tenant page's section. For the super_admin it also holds the
 *  grant and the tenant's monthly pool: edited here and only here (ADR 0138 D16, note of
 *  2026-11-17); a tenant administrator sees them read-only in the status line. onServed hears
 *  whether the CP serves provided routes once the first read answers. */
export function TenantProvidedRoutesView({
  slug,
  onServed,
  ruleStops,
  isSuper = false,
}: {
  slug: string;
  /** The caller is the super_admin: the grant card is drawn (ADR 0138 D16, note of 2026-11-17). */
  isSuper?: boolean;
  onServed?: (served: boolean) => void;
  /** Who the route policy's layers name as stopping the deployment's provided route; empty while
   *  it is allowed, undefined where that is not known. A budget does not reopen it (ADR 0138 D7a:
   *  the policy filters first, the budget only within what it allows). */
  ruleStops?: string;
}) {
  const tr = useT();
  const path = `api/admin/tenants/${encodeURIComponent(slug)}/provided-routes/deployment`;
  const { load, reload, peek } = useProvided(path, parseProvidedTenant);
  const [warnings, setWarnings] = useState<string[]>([]);
  const showRestrictions = useShowView("restrictions");
  // Whether the CP serves provided routes at all, for a page that gives them a tab: a failed read
  // is served (its "could not be read" line has to be reachable), only the plain 404 is not.
  useEffect(() => {
    if (load) onServed?.(load.state !== "off");
  }, [load, onServed]);
  if (!load || load.state === "off") return null;
  if (load.state === "failed") {
    return (
      <section className="admin-panel model-access-provided">
        <h4>{tr("admin.prov_title")}</h4>
        <p className="admin-hint warn" role="alert">
          {load.reason ?? tr("admin.prov_load_failed")}
        </p>
      </section>
    );
  }
  const view = load.value;
  const saved = (w: string[]) => {
    setWarnings(w);
    reload();
  };
  // Every editor is keyed on what it was loaded from, so a reload after a save starts it again
  // from the stored values.
  const key = JSON.stringify([view.members_only, view.member_default_micro]);
  return (
    <section className="admin-panel model-access-provided">
      <h4>{tr("admin.prov_title")}</h4>
      <p className="admin-hint">{tr("admin.prov_note")}</p>
      {ruleStops && (
        <div className="admin-hint warn prov-banner" data-testid="prov-rule-stops">
          <p>{tr("tenant.prov_rule_stops", { who: ruleStops })}</p>
          {showRestrictions && (
            <button type="button" className="btn sm" onClick={showRestrictions}>
              {tr("tenant.prov_see_restrictions")}
            </button>
          )}
        </div>
      )}
      <TenantProvidedSummary view={view} />
      {isSuper && (
        <div className="model-access-provided-grant" data-tenant={slug}>
          <h5 className="admin-subhead">{tr("admin.prov_grant_title")}</h5>
          <GrantEditor
            key={`${view.granted}:${view.ceiling_micro}`}
            path={path}
            view={view}
            peek={peek}
            onSaved={saved}
          />
        </div>
      )}
      <Warnings list={warnings} />
      <PolicyEditor key={key} path={path} view={view} peek={peek} onSaved={saved} />
      <h5 className="admin-subhead">{tr("admin.prov_members")}</h5>
      {view.members.length === 0 ? (
        <p className="muted">{tr("admin.prov_no_members")}</p>
      ) : (
        <table className="admin-table prov-members">
          <thead>
            <tr>
              <th>{tr("tenant.tab_members")}</th>
              <th>{tr("admin.prov_col_granted")}</th>
              <th>{tr("admin.prov_col_budget")}</th>
              <th>{tr("tenant.prov_col_use")}</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {view.members.map((m) => (
              <MemberRow
                key={`${m.membership_id}:${m.granted}:${m.budget_micro}`}
                path={path}
                member={m}
                // MayUse on the CP, under the tenant's grant: not granted, no member's budget applies.
                canUse={view.granted && (m.granted || !view.members_only)}
                peek={peek}
                onSaved={saved}
              />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
