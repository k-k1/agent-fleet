// Per-day, per-box resource history of one member (ADR 0139 decision 3), shown beside the
// uptime heatmap and the cost line. A figure the Agent did not measure is "not measured", never 0.
import { useEffect, useState } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import { figure, fmtBytes, fmtMinutes, fmtPct, fmtRate } from "./usageHistory.ts";
import { widenForTimezone } from "./uptime.ts";
import type { UsageHistoryDay } from "./usageHistory.ts";

/** `from`/`to` (YYYY-MM-DD) follow the grid's applied range; without them the server's default
 * window (the last 30 days) applies. */
export function UsageHistoryTable({ endpoint, from, to }: { endpoint: string; from?: string; to?: string }) {
  const tr = useT();
  const [days, setDays] = useState<UsageHistoryDay[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let alive = true;
    setDays(null);
    setFailed(false);
    const q = new URLSearchParams();
    if (from && to) {
      // Server days are UTC: widen by a day each side so the local edge days of the grid are
      // covered, the same as the grid's own fetch.
      const w = widenForTimezone(from, to);
      q.set("from", w.from);
      q.set("to", w.to);
    } else {
      if (from) q.set("from", from);
      if (to) q.set("to", to);
    }
    api(q.size ? `${endpoint}?${q}` : endpoint)
      .then((d) => {
        if (!alive) return;
        if (d?.error) setFailed(true);
        else setDays(Array.isArray(d?.days) ? d.days : []);
      })
      .catch(() => alive && setFailed(true));
    return () => {
      alive = false;
    };
  }, [endpoint, from, to]);

  const none = tr("usage_history.not_measured");
  const cell = (v: number | null, fmt: (n: number) => string, measured?: number) => {
    const s = figure(v, fmt);
    return (
      <td className={"mono" + (s === null ? " uh-unmeasured" : "")}>
        {s ?? none}
        {s !== null && measured !== undefined && (
          <span className="muted uh-sub"> {tr("usage_history.of_minutes", { n: measured })}</span>
        )}
      </td>
    );
  };

  let body;
  if (failed) body = <p className="form-err">{tr("usage_history.load_error")}</p>;
  else if (days === null) body = <p className="muted">{tr("usage_history.loading")}</p>;
  else if (days.length === 0) body = <p className="muted">{tr("usage_history.empty")}</p>;
  else {
    // Newest day first.
    const rows = [...days].sort((a, b) => (a.day === b.day ? 0 : a.day < b.day ? 1 : -1));
    body = (
      <table className="uh-table" data-testid="usage-history">
        <thead>
          <tr>
            <th>{tr("usage_history.col_day")}</th>
            <th>{tr("usage_history.col_box")}</th>
            <th>{tr("usage_history.col_running")}</th>
            <th>{tr("usage_history.col_cpu_p50")}</th>
            <th>{tr("usage_history.col_cpu_p95")}</th>
            <th>{tr("usage_history.col_ws_p95")}</th>
            <th>{tr("usage_history.col_ws_max")}</th>
            <th>{tr("usage_history.col_psi_peak")}</th>
            <th>{tr("usage_history.col_psi_warn")}</th>
            <th>{tr("usage_history.col_refault")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={`${r.day}|${r.instance_type}|${r.class ?? ""}|${r.override ?? ""}|${r.user_key ?? ""}`}>
              <td className="mono">{r.day}</td>
              <td className="mono">
                {r.instance_type}
                {r.override && <span className="muted uh-sub"> {r.override}</span>}
              </td>
              {cell(r.running_minutes, fmtMinutes)}
              {cell(r.cpu_p50, fmtPct, r.measured_minutes?.cpu)}
              {cell(r.cpu_p95, fmtPct, r.measured_minutes?.cpu)}
              {cell(r.working_set_p95, fmtBytes, r.measured_minutes?.working_set)}
              {cell(r.working_set_max, fmtBytes, r.measured_minutes?.working_set)}
              {cell(r.psi_full_avg60_max, fmtPct, r.measured_minutes?.psi)}
              {cell(r.psi_warn_minutes, fmtMinutes, r.measured_minutes?.psi)}
              {cell(r.refault_p95, fmtRate, r.measured_minutes?.refault)}
            </tr>
          ))}
        </tbody>
      </table>
    );
  }
  return (
    <div className="uh-history-wrap">
      <h5>{tr("usage_history.title")}</h5>
      <p className="muted uh-lede">{tr("usage_history.intro")}</p>
      {body}
    </div>
  );
}
