// Per-member month-by-month table for the admin usage and cloud-cost panels. The server does the
// aggregation (months=N on the admin endpoints, top member_limit members); this only draws it and
// sorts the rows it was given. Month rules are the server's: a no_coverage month is "no data",
// never a zero, and a month with no change figure (first month, coverage-truncated base) shows none.
import { useMemo, useState } from "react";
import { useT } from "../lib/i18n/index.ts";
import { usageMonthRows, costMonthRows, type MonthRow } from "./MonthlyTrend.tsx";

export interface MemberMonths {
  id: string;
  name: string;
  email?: string;
  tenant?: string;
  total: number;
  months: MonthRow[];
}

type SortKey = "name" | "total" | "latest" | "change";

/** Wire rows of `monthly_members` from /api/admin/usage. */
export function usageMemberRows(list: any[] | undefined): MemberMonths[] {
  return (list || []).map((m) => ({
    id: m.membership_id || `${m.tenant}|${m.user_key}`,
    name: m.user_key || "",
    email: m.email,
    tenant: m.tenant,
    total: m.total_secs || 0,
    months: usageMonthRows(m.months),
  }));
}

/** Wire rows of `monthly_members` from /api/admin/cloud-cost. */
export function costMemberRows(list: any[] | undefined): MemberMonths[] {
  return (list || []).map((m) => ({
    id: m.membership_id,
    name: m.user_key || "",
    email: m.email,
    tenant: m.tenant,
    total: m.total_unblended_micro || 0,
    months: costMonthRows(m.months),
  }));
}

const latest = (m: MemberMonths) => {
  const r = m.months[m.months.length - 1];
  return r && !r.noCoverage ? r.value : -1;
};
const latestChange = (m: MemberMonths) => {
  const r = m.months[m.months.length - 1];
  return r && !r.noCoverage && r.changePct != null ? r.changePct : -Infinity;
};

export function MemberMonthlyTable({
  title,
  members,
  totalMembers,
  format,
  showTenant,
  unknownLabel,
}: {
  title: string;
  members: MemberMonths[];
  /** Members the server had before it capped the list. */
  totalMembers?: number;
  format: (v: number) => string;
  showTenant?: boolean;
  unknownLabel: string;
}) {
  const tr = useT();
  const [sort, setSort] = useState<SortKey>("total");
  const rows = useMemo(() => {
    const out = members.slice();
    const cmp: Record<SortKey, (a: MemberMonths, b: MemberMonths) => number> = {
      name: (a, b) => a.name.localeCompare(b.name),
      total: (a, b) => b.total - a.total,
      latest: (a, b) => latest(b) - latest(a),
      change: (a, b) => latestChange(b) - latestChange(a),
    };
    return out.sort((a, b) => cmp[sort](a, b) || a.name.localeCompare(b.name));
  }, [members, sort]);
  if (members.length === 0) return null;
  const heads = members[0].months;
  return (
    <div className="member-monthly">
      <div className="mm-head">
        <h5 className="cc-sub">{title}</h5>
        <label className="mm-sort muted">
          {tr("monthly.sort_by")}{" "}
          <select value={sort} onChange={(e) => setSort(e.target.value as SortKey)}>
            <option value="total">{tr("monthly.sort_total")}</option>
            <option value="latest">{tr("monthly.sort_latest")}</option>
            <option value="change">{tr("monthly.sort_change")}</option>
            <option value="name">{tr("monthly.sort_name")}</option>
          </select>
        </label>
      </div>
      {totalMembers !== undefined && totalMembers > members.length && (
        <p className="muted mt-note">{tr("monthly.members_capped", { shown: members.length, total: totalMembers })}</p>
      )}
      <div className="mm-scroll">
        <table className="mm-table">
          <thead>
            <tr>
              <th>{tr("monthly.col_member")}</th>
              {heads.map((h) => (
                <th key={h.month} className="mono">
                  {h.month}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((m) => (
              <tr key={m.id}>
                <td className="mm-name" title={m.email || ""}>
                  <span className="mono">{m.name || unknownLabel}</span>
                  {showTenant && m.tenant && <span className="muted"> {m.tenant}</span>}
                </td>
                {m.months.map((c) => (
                  <td key={c.month} className={"mm-cell mono" + (c.partial ? " partial" : "")} data-month={c.month}>
                    {c.noCoverage ? (
                      <span className="muted" title={tr("monthly.no_coverage_hint")}>
                        {tr("monthly.no_coverage")}
                      </span>
                    ) : (
                      <>
                        {format(c.value)}
                        {c.changePct != null && (
                          <span className={"mm-chg muted"} title={tr("monthly.vs_prev")}>
                            {(c.changePct > 0 ? "+" : "") + c.changePct.toFixed(1) + "%"}
                          </span>
                        )}
                      </>
                    )}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
