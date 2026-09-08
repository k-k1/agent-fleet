// The occupancy heatmap of one self-hosted inference engine (ADR 0071). 24 hours down, dates
// across — the same shape as the workspace uptime heatmap, and deliberately so: an operator who
// has read one should not have to learn a second grammar to read the other.
//
// It is a separate component rather than a prop on UptimeHeatmap because the cell means
// something else. There are no members to break down and no session counts, and there are two
// states a workspace does not have: the cold start and the drain, both of which bill without
// serving anything. Everything that is genuinely the same — the local-time fold, the colour
// levels, the legend bands, the stylesheet — is imported rather than copied.
//
// Never paint money here (ADR 0048 決定 2). A GPU box has an hourly rate and it is tempting, but
// an hourly amount could only be seconds times a number somebody typed into a parameter once,
// and an estimate rendered as currency is quoted back as a fact. This says how long the
// hardware was up.
//
// ⚠️ Three states, not two: blank = the control plane was not watching, grey = it was watching
// and the engine was down, colour = it was up. Collapsing blank into grey turns a CP outage
// into a confident "the GPU was idle all weekend".
import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "../../../core/api/client.ts";
import { useT } from "../../../lib/i18n/index.ts";
import type { MsgKey } from "../../../lib/i18n/index.ts";
import {
  cellLevel,
  dayRange,
  levelBand,
  localDayKey,
  timezoneAlignsToHours,
  widenForTimezone,
} from "../../usage/uptime.ts";
import {
  buildEngineGrid,
  engineCellState,
  engineCellValue,
  engineMetricSecs,
  splitHM,
} from "./engineUptime.ts";
import type { EngineCell, EngineUptimeMetric, EngineUptimeResponse } from "./engineUptime.ts";
import "../../usage/uptime.css";

const METRICS: [EngineUptimeMetric, MsgKey][] = [
  ["running", "admin.engines_metric_running"],
  ["up", "admin.engines_metric_up"],
];

// 14 local days, the same window the workspace heatmap defaults to. 30 columns crushes the date
// labels into "08-1708-18" (measured on the cloud cost bar chart), and an engine that ran last
// month is a question for the audit log rather than for a status panel.
const DAYS = 14;

// A cell is a percentage of the hour, so the scale is fixed at 1 rather than taken from the
// data. Scaling to the observed maximum would make a week in which the engine never exceeded
// 10% of an hour look identical to a week it ran flat out — the colour would encode "compared
// with your own best hour" instead of "how much of the hour".
const FULL = 1;

function defaultRange(): { from: string; to: string } {
  const now = new Date();
  return {
    from: localDayKey(new Date(now.getTime() - (DAYS - 1) * 86400000)),
    to: localDayKey(now),
  };
}

const pct = (v: number) => Math.round(v * 100) + "%";

/** The separator between two facts on one status line.
 *
 * It is CSS content rather than a literal in the JSX because the mark that reads best here is
 * "・", a Japanese punctuation character: written inline it is a bare Japanese literal, which
 * `npm run i18n:lint` rejects (rightly — it would never be translated). Putting a pure
 * separator in the catalogue would instead add a translatable key carrying no meaning, and one
 * more thing for a translator to get wrong. A decorative separator belongs in the stylesheet,
 * and `aria-hidden` keeps it out of what a screen reader announces. */
export const Sep = () => <span className="engines-sep" aria-hidden="true" />;


/** Duration in the reader's units. Hours appear only once there are any, so a 4-minute start
 *  does not read as "0 時間 4 分". */
export function useDuration(): (secs: number) => string {
  const tr = useT();
  return (secs: number) => {
    const { hours, mins } = splitHM(secs);
    return hours > 0
      ? tr("admin.engines_dur_hm").replace("{h}", String(hours)).replace("{m}", String(mins))
      : tr("admin.engines_dur_m").replace("{m}", String(mins));
  };
}

export function EngineUptimePanel({ engineKey }: { engineKey: string }) {
  const tr = useT();
  const dur = useDuration();
  const [data, setData] = useState<EngineUptimeResponse | null>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(true);
  const [metric, setMetric] = useState<EngineUptimeMetric>("running");
  const [showTable, setShowTable] = useState(false);
  const [hover, setHover] = useState<{ day: string; hour: number } | null>(null);
  const [range] = useState(defaultRange);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      // The server cuts on UTC days, so the local edge cells need one extra day at each end
      // (at +09:00, 15:00 UTC is the local next-day 00:00 cell).
      const w = widenForTimezone(range.from, range.to);
      const p = new URLSearchParams({ from: w.from, to: w.to });
      const d = await api(`api/admin/engines/${encodeURIComponent(engineKey)}/hourly?${p}`);
      if (d?.error) {
        setErr(tr("admin.engines_uptime_error"));
        setData(null);
      } else {
        setErr("");
        setData(d as EngineUptimeResponse);
      }
    } catch {
      setErr(tr("admin.engines_uptime_error"));
      setData(null);
    } finally {
      setLoading(false);
    }
  }, [engineKey, range, tr]);

  useEffect(() => {
    load();
  }, [load]);

  const grid = useMemo(() => buildEngineGrid(data), [data]);
  const days = useMemo(() => dayRange(range.from, range.to), [range]);
  const hovered = hover ? grid.get(hover.day + "|" + hover.hour) : undefined;
  // The floor of 2 is about column width, not about label count: "08-19" is about 45px wide
  // against a 22px column, so stride 1 makes the labels touch.
  const stride = Math.max(2, Math.ceil(days.length / 8));

  if (loading && !data) return <p className="muted">{tr("common.loading")}</p>;

  return (
    <div className="uptime-heatmap">
      <div className="uh-controls">
        <div className="uh-metrics" role="group" aria-label={tr("admin.engines_metric_label")}>
          {METRICS.map(([m, label]) => (
            <button
              key={m}
              type="button"
              className={"uh-metric" + (metric === m ? " active" : "")}
              aria-pressed={metric === m}
              onClick={() => setMetric(m)}
            >
              {tr(label)}
            </button>
          ))}
        </div>
        <button type="button" className="uh-tablebtn" onClick={() => setShowTable((v) => !v)}>
          {showTable ? tr("uptime.show_map") : tr("uptime.show_table")}
        </button>
      </div>

      {/* The legend is not decoration: as soon as colour carries meaning it is the minimum
          needed for the meaning not to be carried by colour alone. */}
      <div className="uh-legend">
        <span className="uh-lg-item">
          <span className="uh-swatch uh-unobserved" />
          {tr("uptime.state_unobserved")}
        </span>
        <span className="uh-lg-item">
          <span className="uh-swatch uh-stopped" />
          {tr("admin.engines_state_down")}
        </span>
        <span className="uh-lg-scale">
          {[1, 2, 3, 4, 5].map((lv) => {
            const [lo, hi] = levelBand(lv, FULL, false);
            return <span key={lv} className={"uh-swatch uh-lv-" + lv} title={`${pct(lo)}–${pct(hi)}`} />;
          })}
          <span className="uh-lg-max muted">{pct(FULL)}</span>
        </span>
      </div>

      {err && <p className="form-err">{err}</p>}

      {showTable ? (
        <EngineTable grid={grid} days={days} metric={metric} dur={dur} />
      ) : (
        <>
          <div
            className="uh-grid"
            style={{
              gridTemplateColumns: `var(--uh-gutter) repeat(${days.length}, minmax(8px, var(--uh-cell-w)))`,
            }}
            onMouseLeave={() => setHover(null)}
          >
            <div className="uh-corner" />
            {days.map((d, i) => (
              <div key={d} className="uh-colhead muted" title={d}>
                {i % stride === 0 ? d.slice(5) : ""}
              </div>
            ))}
            {Array.from({ length: 24 }, (_, h) => (
              <EngineRow
                key={h}
                hour={h}
                days={days}
                grid={grid}
                metric={metric}
                onHover={setHover}
              />
            ))}
          </div>
          <div className="uh-foot">
            {hover ? (
              <EngineReadout cell={hovered} day={hover.day} hour={hover.hour} metric={metric} dur={dur} />
            ) : (
              <p className="muted uh-hint">{tr("uptime.hint")}</p>
            )}
          </div>
        </>
      )}

      <p className="muted uh-note">
        {tr("admin.engines_uptime_note").replace("{n}", String(Math.round(data?.interval_secs || 0)))}
        {!timezoneAlignsToHours() && " " + tr("uptime.note_halfhour")}
      </p>
    </div>
  );
}

function EngineRow({
  hour,
  days,
  grid,
  metric,
  onHover,
}: {
  hour: number;
  days: string[];
  grid: Map<string, EngineCell>;
  metric: EngineUptimeMetric;
  onHover: (v: { day: string; hour: number } | null) => void;
}) {
  const tr = useT();
  return (
    <>
      <div className="uh-rowhead muted">{hour % 3 === 0 ? String(hour).padStart(2, "0") : ""}</div>
      {days.map((day) => {
        const c = grid.get(day + "|" + hour);
        const st = engineCellState(c, metric);
        const cls =
          st === "unobserved"
            ? "uh-cell uh-unobserved"
            : st === "stopped"
              ? "uh-cell uh-stopped"
              : "uh-cell uh-lv-" + cellLevel(engineCellValue(c, metric), FULL, false);
        // State is never carried by colour alone: the aria-label lets a screen reader announce
        // "9/1 10:00 記録なし" for a cell that is merely blank.
        const label =
          st === "unobserved"
            ? tr("uptime.state_unobserved")
            : st === "stopped"
              ? tr("admin.engines_state_down")
              : pct(engineCellValue(c, metric));
        return (
          <div
            key={day}
            className={cls}
            tabIndex={0}
            role="img"
            aria-label={`${day} ${String(hour).padStart(2, "0")}:00 ${label}`}
            onMouseEnter={() => onHover({ day, hour })}
            onFocus={() => onHover({ day, hour })}
          />
        );
      })}
    </>
  );
}

/** Hover / focus readout. The breakdown into running / starting / draining is the point of
 *  hovering: the colour says how much of the hour cost money, and only this says how much of
 *  that was actually answering requests. */
function EngineReadout({
  cell,
  day,
  hour,
  metric,
  dur,
}: {
  cell?: EngineCell;
  day: string;
  hour: number;
  metric: EngineUptimeMetric;
  dur: (secs: number) => string;
}) {
  const tr = useT();
  const hh = String(hour).padStart(2, "0");
  const st = engineCellState(cell, metric);
  return (
    <div className="uh-readout">
      <span className="uh-ro-when mono">
        {day} {hh}:00
      </span>
      {st !== "running" ? (
        <span className="uh-ro-val">
          {st === "unobserved" ? tr("uptime.state_unobserved") : tr("admin.engines_state_down")}
        </span>
      ) : (
        <>
          <span className="uh-ro-val mono">
            {pct(engineCellValue(cell, metric))}
            <Sep />
            {dur(engineMetricSecs(cell, metric))}
          </span>
          <span className="uh-ro-sub muted">
            {tr("admin.engines_ro_detail")
              .replace("{run}", dur(cell!.runningSecs))
              .replace("{start}", dur(cell!.startingSecs))
              .replace("{drain}", dur(cell!.drainingSecs))}
          </span>
        </>
      )}
    </div>
  );
}

/** Table view. The escape from carrying values in colour alone, not a nicety — it is what
 *  reaches a reader who cannot tell the shades apart. */
function EngineTable({
  grid,
  days,
  metric,
  dur,
}: {
  grid: Map<string, EngineCell>;
  days: string[];
  metric: EngineUptimeMetric;
  dur: (secs: number) => string;
}) {
  const tr = useT();
  const rows: { day: string; hour: number; cell: EngineCell }[] = [];
  for (const day of days) {
    for (let h = 0; h < 24; h++) {
      const c = grid.get(day + "|" + h);
      if (c && engineMetricSecs(c, metric) > 0) rows.push({ day, hour: h, cell: c });
    }
  }
  if (rows.length === 0) return <p className="muted">{tr("admin.engines_uptime_none")}</p>;
  return (
    <table className="uh-table">
      <thead>
        <tr>
          <th>{tr("uptime.col_when")}</th>
          <th>{tr("admin.engines_col_running")}</th>
          <th>{tr("admin.engines_col_starting")}</th>
          <th>{tr("admin.engines_col_draining")}</th>
          <th>{tr("uptime.col_value")}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map(({ day, hour, cell }) => (
          <tr key={day + hour}>
            <td className="mono">
              {day} {String(hour).padStart(2, "0")}:00
            </td>
            <td className="mono">{dur(cell.runningSecs)}</td>
            <td className="mono">{dur(cell.startingSecs)}</td>
            <td className="mono">{dur(cell.drainingSecs)}</td>
            <td className="mono">{pct(engineCellValue(cell, metric))}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
