// Month-by-month totals with the change against the previous month. The server aggregates
// (months=N on the usage and cost endpoints) and decides which months are partial or have no data;
// this only draws them. A month with no_coverage is "no data", never a zero bar: cost allocation is
// not retroactive, so a month before it is unknown.
import { useT } from "../lib/i18n/index.ts";

export interface MonthRow {
  month: string;
  /** Seconds for hours, micro-units for money. */
  value: number;
  partial?: boolean;
  /** First covered day of a finished month cut short by where the data starts (not still running). */
  coverageFrom?: string;
  noCoverage?: boolean;
  estimated?: boolean;
  /** Percent against the previous month; absent when there is nothing to compare with. */
  changePct?: number | null;
  /** Shared bucket, super_admin deployment view only. */
  sharedLabel?: string;
}

export function MonthlyTrend({
  title,
  rows,
  format,
  note,
}: {
  title: string;
  rows: MonthRow[];
  format: (v: number) => string;
  note?: string;
}) {
  const tr = useT();
  if (rows.length === 0) return null;
  const max = rows.reduce((m, r) => Math.max(m, r.noCoverage ? 0 : r.value), 0);
  return (
    <div className="monthly-trend">
      <h5 className="cc-sub">{title}</h5>
      <p className="muted mt-note">
        {tr("monthly.note_utc")} {note}
      </p>
      <div className="mt-rows">
        {rows.map((r) => {
          const cls = "mt-row" + (r.partial ? " partial" : "") + (r.noCoverage ? " nodata" : "");
          return (
            <div key={r.month} className={cls} data-month={r.month}>
              <span className="mt-month mono">{r.month}</span>
              <span className="mt-bar">
                <span className="mt-fill" style={{ width: (max && !r.noCoverage ? Math.round((r.value / max) * 100) : 0) + "%" }} />
              </span>
              {r.noCoverage ? (
                <span className="mt-val muted" title={tr("monthly.no_coverage_hint")}>
                  {tr("monthly.no_coverage")}
                </span>
              ) : (
                <span className="mt-val mono">
                  {format(r.value)}
                  {r.estimated && <span className="muted"> {tr("monthly.estimated")}</span>}
                  {r.partial && (
                    <span className="muted" title={r.noCoverage === undefined ? undefined : tr("monthly.partial_cost_hint")}>
                      {" "}({r.coverageFrom ? tr("monthly.partial_coverage", { day: r.coverageFrom }) : tr("monthly.partial")})
                    </span>
                  )}
                </span>
              )}
              <span className="mt-chg mono muted" title={tr("monthly.vs_prev")}>
                {r.changePct == null || r.noCoverage ? "" : (r.changePct > 0 ? "+" : "") + r.changePct.toFixed(1) + "%"}
              </span>
              {r.sharedLabel && <span className="mt-shared muted">{r.sharedLabel}</span>}
            </div>
          );
        })}
      </div>
    </div>
  );
}

/** Wire rows from /api/admin/usage and /api/usage/me/monthly (running_secs). */
export function usageMonthRows(monthly: any[] | undefined): MonthRow[] {
  return (monthly || []).map((m) => ({
    month: m.month,
    value: m.running_secs || 0,
    partial: !!m.partial,
    changePct: m.change_pct ?? null,
  }));
}

/** Wire rows from the cost endpoints (unblended_micro); fmt renders the shared figure. */
export function costMonthRows(monthly: any[] | undefined, sharedFmt?: (v: number) => string): MonthRow[] {
  return (monthly || []).map((m) => ({
    month: m.month,
    value: m.unblended_micro || 0,
    partial: !!m.partial,
    coverageFrom: m.coverage_from || undefined,
    noCoverage: !!m.no_coverage,
    estimated: !!m.estimated,
    changePct: m.change_pct ?? null,
    sharedLabel: m.shared_micro !== undefined && sharedFmt && !m.no_coverage ? sharedFmt(m.shared_micro) : undefined,
  }));
}
