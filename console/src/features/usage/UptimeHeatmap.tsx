// Uptime heatmap — 24 hours down, dates across (docs/log/83).
//
// Why it exists: uptime and cloud cost each carry one number per day, which is enough to see
// "Fridays are high" but not whether someone left a workspace running or was actually working.
// Splitting the cells down to the hour makes the two distinguishable by shape (a faint orange
// band all night = left running; dark only during the day = working).
//
// Never paint money here. Real cost (Cost Explorer) is only available per day, so an hourly
// amount could only be seconds times a unit price someone typed in once — an estimate, which ADR
// 0048 decision 2 rejects. That is also why this is the uptime view and not the cloud cost view.
//
// A cell has three states, not two: unobserved / stopped / running. Grey means "it was stopped",
// blank means "the CP was not watching". Collapsing that to two makes a day when the CP was down
// display confidently as a day when nobody worked.
import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { api } from "../../core/api/client.ts";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import {
  buildGrid,
  cellLevel,
  cellValue,
  dayRange,
  isUnmeasured,
  levelBand,
  localDayKey,
  maxValue,
  minutesOf,
  timezoneAlignsToHours,
  widenForTimezone,
} from "./uptime.ts";
import type { UptimeCell, UptimeMetric, UptimeResponse } from "./uptime.ts";
import "./uptime.css";

const METRICS: [UptimeMetric, MsgKey][] = [
  ["sessions", "uptime.metric_sessions"],
  ["busy", "uptime.metric_busy"],
  ["running", "uptime.metric_running"],
];

const DEFAULT_DAYS = 14;

// The default range is the last 14 local days. 30 days x 24 rows packs the columns so tightly
// that the date labels run together as "08-1708-18", as the cloud cost bar chart did.
function defaultRange(): { from: string; to: string } {
  const now = new Date();
  return {
    from: localDayKey(new Date(now.getTime() - (DEFAULT_DAYS - 1) * 86400000)),
    to: localDayKey(now),
  };
}

/** Value formatting. The legend, the readout and the table all go through this one function so
 * the same number never appears in two shapes.
 *
 * A single member's running ratio is capped at 100%: in an hour where the sweep failed part way
 * through, a member's samples can exceed the heartbeat count, and dividing straight through
 * reports "103% running". */
function fmtValue(v: number, metric: UptimeMetric, aggregate: boolean): string {
  if (metric !== "running") return v.toFixed(1);
  if (aggregate) return v.toFixed(1);
  return Math.round(Math.min(1, v) * 100) + "%";
}

// Thinning of the date labels.
//
// The floor of 2 is about column width, not about how many labels there are: "08-19" is about
// 45px wide while a column is only 22px, so a three-day range would give stride 1 and the labels
// would touch — the same break as the cloud cost bar chart's "08-1708-18".
function labelStride(n: number): number {
  return Math.max(2, Math.ceil(n / 8));
}

/** Range-scoped fetch. As in the cost view, only Apply refetches: a date input changes on every
 * keystroke, so putting it in the dependencies fires a request while the user is still typing. */
function useUptime(endpoint: string, tenant?: string) {
  const tr = useT();
  const [range, setRange] = useState(defaultRange);
  const [applied, setApplied] = useState(range);
  const [data, setData] = useState<UptimeResponse | null>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(
    async (win: { from: string; to: string }) => {
      setLoading(true);
      setErr("");
      try {
        // The server cuts on UTC days, so filling the cells at the local edges needs one extra
        // day on each side (at +09:00, 15:00 UTC is the local next-day 00:00 cell).
        const w = widenForTimezone(win.from, win.to);
        // The query is assembled here and nowhere else. If callers appended `?tenant=` to
        // endpoint instead, a broken `?tenant=x?from=y` URL would pass silently.
        const p = new URLSearchParams({ from: w.from, to: w.to });
        if (tenant) p.set("tenant", tenant);
        const d = await api(`${endpoint}?${p.toString()}`);
        if (d?.error) {
          setErr(tr("uptime.load_error"));
          setData(null);
        } else {
          setData(d as UptimeResponse);
          setApplied(win);
        }
      } catch {
        setErr(tr("uptime.load_error"));
        setData(null);
      } finally {
        setLoading(false);
      }
    },
    [endpoint, tenant, tr],
  );

  // Only Apply refetches on a range change, because a date input changes per keystroke. Switching
  // tenant refetches immediately: that one is a settled choice.
  useEffect(() => {
    load(defaultRange());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint, tenant]);

  return { range, setRange, applied, data, err, loading, apply: () => load(range) };
}

/** Hover / focus readout. Value first, label second: the reader already knows where they are and
 * wants the number. The tooltip is a supplement, never the only path — the table view carries the
 * same content. */
function CellReadout({
  cell,
  day,
  hour,
  metric,
  aggregate,
}: {
  cell?: UptimeCell;
  day: string;
  hour: number;
  metric: UptimeMetric;
  aggregate: boolean;
}) {
  const tr = useT();
  const hh = String(hour).padStart(2, "0");
  if (!cell?.observed && !cell?.runningSecs) {
    return (
      <div className="uh-readout">
        <span className="uh-ro-when mono">
          {day} {hh}:00
        </span>
        <span className="uh-ro-val">{tr("uptime.state_unobserved")}</span>
      </div>
    );
  }
  const running = cell.runningSecs > 0;
  const unmeasured = isUnmeasured(cell, metric);
  const value = cellValue(cell, metric);
  return (
    <div className="uh-readout">
      <span className="uh-ro-when mono">
        {day} {hh}:00
      </span>
      {!running ? (
        <span className="uh-ro-val">{tr("uptime.state_stopped")}</span>
      ) : (
        <>
          <span className="uh-ro-val mono">
            {metric === "running"
              ? aggregate
                ? tr("uptime.ro_workspaces").replace("{n}", value.toFixed(1))
                : tr("uptime.ro_minutes").replace("{n}", String(minutesOf(cell.runningSecs)))
              : unmeasured
                ? tr("uptime.state_unmeasured")
                : tr("uptime.ro_sessions").replace("{n}", value.toFixed(1))}
          </span>
          <span className="uh-ro-sub muted">
            {tr("uptime.ro_detail")
              .replace("{min}", String(minutesOf(cell.runningSecs)))
              .replace("{peak}", String(metric === "busy" ? cell.maxBusy : cell.maxSessions))}
          </span>
          {aggregate && cell.contributions.length > 0 && (
            <span className="uh-ro-top muted">
              {cell.contributions
                .slice(0, 3)
                .map((c) => c.label)
                .join(" / ")}
              {cell.contributions.length > 3 &&
                " " + tr("uptime.ro_more").replace("{n}", String(cell.contributions.length - 3))}
            </span>
          )}
        </>
      )}
    </div>
  );
}

/** Legend. As soon as colour distinguishes more than one meaning the legend is always shown; it
 * is the minimum needed so that colour is never the only carrier of meaning. */
function Legend({ max, metric, aggregate }: { max: number; metric: UptimeMetric; aggregate: boolean }) {
  const tr = useT();
  const zeroFloor = metric !== "running";
  const fmt = (v: number) => fmtValue(v, metric, aggregate);
  return (
    <div className="uh-legend">
      <span className="uh-lg-item">
        <span className="uh-swatch uh-unobserved" />
        {tr("uptime.state_unobserved")}
      </span>
      <span className="uh-lg-item">
        <span className="uh-swatch uh-stopped" />
        {tr("uptime.state_stopped")}
      </span>
      <span className="uh-lg-scale">
        {[1, 2, 3, 4, 5].map((lv) => {
          const [lo, hi] = levelBand(lv, max, zeroFloor);
          return (
            <span
              key={lv}
              className={"uh-swatch uh-lv-" + lv}
              title={zeroFloor && lv === 1 ? tr("uptime.legend_zero") : `${fmt(lo)}–${fmt(hi)}`}
            />
          );
        })}
        <span className="uh-lg-max muted">{max > 0 ? fmt(max) : ""}</span>
      </span>
    </div>
  );
}

/** Table view. It is the escape from carrying values in colour alone, not a nicety: the legend,
 * the readout and this table together reach a reader who cannot tell the colours apart. */
function UptimeTable({
  grid,
  days,
  metric,
  aggregate,
}: {
  grid: Map<string, UptimeCell>;
  days: string[];
  metric: UptimeMetric;
  aggregate: boolean;
}) {
  const tr = useT();
  const rows: { day: string; hour: number; cell: UptimeCell }[] = [];
  for (const day of days) {
    for (let h = 0; h < 24; h++) {
      const c = grid.get(day + "|" + h);
      if (c && c.runningSecs > 0) rows.push({ day, hour: h, cell: c });
    }
  }
  if (rows.length === 0) return <p className="muted">{tr("uptime.no_records")}</p>;
  return (
    <table className="uh-table">
      <thead>
        <tr>
          <th>{tr("uptime.col_when")}</th>
          <th>{tr("uptime.col_running")}</th>
          <th>{tr("uptime.col_value")}</th>
          <th>{tr("uptime.col_peak")}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map(({ day, hour, cell }) => (
          <tr key={day + hour}>
            <td className="mono">
              {day} {String(hour).padStart(2, "0")}:00
            </td>
            <td className="mono">{minutesOf(cell.runningSecs)}</td>
            <td className="mono">
              {isUnmeasured(cell, metric)
                ? tr("uptime.state_unmeasured")
                : fmtValue(cellValue(cell, metric), metric, aggregate)}
            </td>
            <td className="mono">{metric === "busy" ? cell.maxBusy : cell.maxSessions}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** The heatmap itself. Fetching is the caller's job; all three entry points return the same
 * response shape.
 *
 * aggregate=true is the admin roll-up: a cell's value becomes the average number of workspaces
 * running in that hour, with the breakdown on hover. It takes the per-member series and sums them
 * here rather than taking a pre-summed total, so the total and its breakdown cannot come from two
 * APIs and disagree. */
export function UptimeHeatmap({
  data,
  from,
  to,
  aggregate = false,
}: {
  data: UptimeResponse | null;
  from: string;
  to: string;
  aggregate?: boolean;
}) {
  const tr = useT();
  const [metric, setMetric] = useState<UptimeMetric>("sessions");
  const [showTable, setShowTable] = useState(false);
  const [hover, setHover] = useState<{ day: string; hour: number } | null>(null);

  const grid = useMemo(() => buildGrid(data), [data]);
  const days = useMemo(() => dayRange(from, to), [from, to]);
  const max = useMemo(() => maxValue(grid, days, metric), [grid, days, metric]);
  const zeroFloor = metric !== "running";
  const stride = labelStride(days.length);
  const hovered = hover ? grid.get(hover.day + "|" + hover.hour) : undefined;

  return (
    <div className="uptime-heatmap">
      <div className="uh-controls">
        <div className="uh-metrics" role="group" aria-label={tr("uptime.metric_label")}>
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

      <Legend max={max} metric={metric} aggregate={aggregate} />

      {showTable ? (
        <UptimeTable grid={grid} days={days} metric={metric} aggregate={aggregate} />
      ) : (
        <>
          <div
            className="uh-grid"
            // Not 1fr: stretching 14 columns across the full width turns each cell into a
            // horizontal bar and it stops reading as a heatmap (measured headless). Cap the
            // width and align left instead.
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
              <Row
                key={h}
                hour={h}
                days={days}
                grid={grid}
                metric={metric}
                max={max}
                zeroFloor={zeroFloor}
                aggregate={aggregate}
                onHover={setHover}
              />
            ))}
          </div>
          <div className="uh-foot">
            {hover ? (
              <CellReadout
                cell={hovered}
                day={hover.day}
                hour={hover.hour}
                metric={metric}
                aggregate={aggregate}
              />
            ) : (
              <p className="muted uh-hint">{tr("uptime.hint")}</p>
            )}
          </div>
        </>
      )}

      {/* These caveats are not footnotes: both the sampling-interval coarseness and the
          half-hour-offset timezones change how a cell must be read. */}
      <p className="muted uh-note">
        {tr("uptime.note_sampling").replace("{n}", String(Math.round((data?.interval_secs || 0) / 60)))}
        {!timezoneAlignsToHours() && " " + tr("uptime.note_halfhour")}
      </p>
    </div>
  );
}

function Row({
  hour,
  days,
  grid,
  metric,
  max,
  zeroFloor,
  aggregate,
  onHover,
}: {
  hour: number;
  days: string[];
  grid: Map<string, UptimeCell>;
  metric: UptimeMetric;
  max: number;
  zeroFloor: boolean;
  aggregate: boolean;
  onHover: (v: { day: string; hour: number } | null) => void;
}) {
  const tr = useT();
  return (
    <>
      <div className="uh-rowhead muted">{hour % 3 === 0 ? String(hour).padStart(2, "0") : ""}</div>
      {days.map((day) => {
        const c = grid.get(day + "|" + hour);
        const running = (c?.runningSecs || 0) > 0;
        const observed = !!c?.observed || running;
        const cls = !observed
          ? "uh-cell uh-unobserved"
          : !running
            ? "uh-cell uh-stopped"
            : "uh-cell uh-lv-" + cellLevel(cellValue(c, metric), max, zeroFloor) +
              (isUnmeasured(c, metric) ? " uh-unmeasured" : "");
        // State is never carried by colour alone: putting the timestamp and state in aria-label
        // lets a screen reader announce "9/1 10:00 stopped".
        const state = !observed
          ? tr("uptime.state_unobserved")
          : !running
            ? tr("uptime.state_stopped")
            : isUnmeasured(c, metric)
              ? tr("uptime.state_unmeasured")
              : fmtValue(cellValue(c, metric), metric, aggregate);
        return (
          <div
            key={day}
            className={cls}
            tabIndex={0}
            role="img"
            aria-label={`${day} ${String(hour).padStart(2, "0")}:00 ${state}`}
            onMouseEnter={() => onHover({ day, hour })}
            onFocus={() => onHover({ day, hour })}
          />
        );
      })}
    </>
  );
}

/** Range inputs, in the same order as the cost view: dates, then Apply. */
function UptimeRangeBar({
  range,
  setRange,
  onApply,
  loading,
  children,
}: {
  range: { from: string; to: string };
  setRange: (v: { from: string; to: string }) => void;
  onApply: () => void;
  loading: boolean;
  children?: ReactNode;
}) {
  const tr = useT();
  return (
    <div className="usage-toolbar">
      <label>
        {tr("admin.from")}
        <input
          type="date"
          value={range.from}
          onChange={(e) => setRange({ ...range, from: e.target.value })}
        />
      </label>
      <label>
        {tr("admin.to")}
        <input
          type="date"
          value={range.to}
          onChange={(e) => setRange({ ...range, to: e.target.value })}
        />
      </label>
      {children}
      <button className="primary" onClick={onApply} disabled={loading}>
        {loading ? "…" : tr("admin.apply")}
      </button>
    </div>
  );
}

/** MyUptimeView — the uptime tab in the user's own settings.
 *
 * Not one other person's uptime reaches this view; the CP returns only the caller's own. */
export function MyUptimeView() {
  const tr = useT();
  const { range, setRange, applied, data, err, loading, apply } = useUptime("api/usage/me/hourly");
  return (
    <div className="admin-stage">
      <section className="admin-panel">
        <h4>{tr("uptime.my_title")}</h4>
        <p className="muted uh-lede">{tr("uptime.my_intro")}</p>
        <UptimeRangeBar range={range} setRange={setRange} onApply={apply} loading={loading} />
        {err && <p className="form-err">{err}</p>}
      </section>
      <section className="admin-panel">
        <UptimeHeatmap data={data} from={applied.from} to={applied.to} />
      </section>
    </div>
  );
}

/** MemberUptimePanel — the panel embedded in the admin member detail view.
 *
 * It sits next to force-stop workspace and the disk quota, so "a faint orange band all night",
 * i.e. left running, is directly the justification for those actions. */
export function MemberUptimePanel({ slug, userKey }: { slug: string; userKey: string }) {
  const tr = useT();
  const endpoint = `api/admin/tenants/${encodeURIComponent(slug)}/members/${encodeURIComponent(userKey)}/usage-hourly`;
  const { range, setRange, applied, data, err, loading, apply } = useUptime(endpoint);
  return (
    <section className="admin-panel">
      <h4>{tr("uptime.member_title")}</h4>
      <p className="muted uh-lede">{tr("uptime.member_intro")}</p>
      <UptimeRangeBar range={range} setRange={setRange} onApply={apply} loading={loading} />
      {err && <p className="form-err">{err}</p>}
      <UptimeHeatmap data={data} from={applied.from} to={applied.to} />
    </section>
  );
}

/** UptimeAdminView — the roll-up panel on the admin uptime view. `tenant` is the slug of the
 * tenant selection (empty = the whole deployment, super_admin only). */
export function UptimeAdminView({ tenant, children }: { tenant?: string; children?: ReactNode }) {
  const tr = useT();
  const { range, setRange, applied, data, err, loading, apply } = useUptime(
    "api/admin/usage/hourly",
    tenant,
  );
  return (
    <section className="admin-panel">
      <h4>{tr("uptime.admin_title")}</h4>
      <p className="muted uh-lede">{tr("uptime.admin_intro")}</p>
      <UptimeRangeBar range={range} setRange={setRange} onApply={apply} loading={loading}>
        {children}
      </UptimeRangeBar>
      {err && <p className="form-err">{err}</p>}
      <UptimeHeatmap data={data} from={applied.from} to={applied.to} aggregate />
    </section>
  );
}
