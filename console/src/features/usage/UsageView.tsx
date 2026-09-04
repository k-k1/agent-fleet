// UsageView — the per-feature token usage dashboard (docs/log/46 P4 / ADR0029 §7-5).
//
// Written as a pure component with no dependency on the modal. Today the usage tab of the
// settings modal only wraps it thinly; if it is ever promoted to a pane, adding a PaneKind is
// enough to mount the same view. The reverse does not work — a pane-only component cannot go
// into settings — hence this order.
//
// It reads GET /api/usage/series, already aggregated server-side; raw logs never arrive here.
// Three requests per screen: the time series for the selected axis, feature x model and
// agent x model. The last two are matrices, so the breakdowns (by feature, by model, by agent)
// are derived from them. In addition the rtk savings card at the bottom reads a separate lineage
// once (GET /api/agents/rtk/gain; see RtkGainCard).
//
// Display guarantees (the non-negotiable lines of docs/log/46 §1-c):
//   * Never let zero be confused with unmeasured. A CLI that reports no tokens comes out at
//     spend 0, which does not mean it was unused. The unmeasured call count gets its own tile,
//     always accompanied by a note generated from coverage (a hand-written table drifts).
//   * Cost is a secondary metric. Only claude returns a measured value, so it is labelled as an
//     API-equivalent amount and shown small. The primary metric is spend (= in + ccreate + out).
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errText, isTransientErr } from "../../core/api/client.ts";
import { useRetryLoad } from "../../lib/retryLoad.ts";
import { useWorkspaceStore, wsStartBusy } from "../../core/store/workspace.ts";
import { useT, tMaybe } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { fmtDateTime, fmtNum, TIME_HM } from "../../lib/intl.ts";
import { fmtTok } from "../../lib/fmttok.ts";
import { EmptyState } from "../../ui/EmptyState.tsx";
import { Button } from "../../ui/Button.tsx";
import { Icon } from "../../ui/Icon.tsx";
import { fetchRtkGain, fetchUsageSeries } from "./api.ts";
import type { RtkGain, RtkGainBucket, UsageAgg, UsageCatalog, UsageDim, UsagePrice, UsageSeries } from "./api.ts";
import { OTHER_KEY } from "./colors.ts";
import {
  breakdownRows,
  coverageNotes,
  filterParam,
  foldedFilterOn,
  matrixRows,
  perCall,
  rangeOf,
  stackModel,
  toggleFoldedFilter,
} from "./series.ts";
import type { FilterTerm, UsageMetric } from "./series.ts";

// Range presets (dataviz: the date range is the first filter a reader reaches for).
const RANGES: { hours: number; key: MsgKey }[] = [
  { hours: 24, key: "usage.range_24h" },
  { hours: 24 * 7, key: "usage.range_7d" },
  { hours: 24 * 30, key: "usage.range_30d" },
];

// How the time series is split. Listing origin is the point of docs/log/46 §2-c: it puts
// sessions a person started and sessions the operator or the scheduler raised unattended into the
// same picture.
const BY_DIMS: { dim: UsageDim; key: MsgKey }[] = [
  { dim: "feature", key: "usage.by_feature" },
  { dim: "kind", key: "usage.by_kind" },
  { dim: "model", key: "usage.by_model" },
  { dim: "origin", key: "usage.by_origin" },
  { dim: "trigger", key: "usage.by_trigger" },
];

// Refetch while waiting for folding. Measured at over ten seconds (~20s for 158 sessions), so
// wait 2s x 30 = up to a minute. Hitting the cap does not break the display; only the badge that
// declares folding stays up.
const FOLD_POLL_MS = 2000;
const FOLD_POLL_MAX = 30;

const METRICS: { metric: UsageMetric; key: MsgKey }[] = [
  { metric: "spend", key: "usage.metric_spend" },
  { metric: "calls", key: "usage.metric_calls" },
  { metric: "cread", key: "usage.metric_cread" },
  // The money metric is the estimate: the sessions themselves carry no measured cost and that
  // column used to be permanently "—". The measured value is kept, shown next to the estimate in
  // a tooltip, and never added to it — they are different ways of measuring.
  { metric: "cost_est_usd", key: "usage.metric_cost" },
];

/** Axis value to display name. An unknown value, such as a new model name, shows as its key. */
export const dimLabel = (dim: string, key: string): string => {
  if (key === OTHER_KEY) return tMaybe("usage.other") ?? "other";
  if (key === "") return tMaybe("usage.empty_value") ?? "—";
  return tMaybe(`usage.val.${dim}.${key}`) ?? key;
};

// Writing 0 as "$0.0000" reads as "it ran for free", so no value shows as "—", the same as
// unmeasured.
const fmtUSD = (v: number): string => (v <= 0 ? "—" : v >= 1 ? "$" + v.toFixed(2) : "$" + v.toFixed(4));

// An estimate always carries the approximation sign. Set in the same type as the measured value
// (claude's auxiliary calls), a number derived from the price table times the tokens gets read as
// a bill.
export const fmtUSDEst = (v: number): string => (v <= 0 ? "—" : "≈" + fmtUSD(v));

// Unit price ($ per million tokens). Trailing zeros are dropped: $2.00 next to $0.0200 is hard
// to read.
const fmtRate = (v: number): string => "$" + String(v >= 1 ? +v.toFixed(2) : +v.toFixed(4));

/** The price source on one line. For catalog:<provider>/<model> the provider is shown too, as a
 * handle for checking the arithmetic. */
export function priceSrcLine(price: UsagePrice | undefined): string {
  if (!price) return "";
  const [kind, ref] = price.src.split(/:(.+)/);
  return kind === "catalog"
    ? (tMaybe("usage.price_src_catalog") ?? "").replace("{ref}", ref || "")
    : (tMaybe("usage.price_src_builtin") ?? "");
}

/** Tooltip of a money cell: unit price, source and the measured value, because an amount alone
 * cannot be checked. */
export function costCellTitle(agg: UsageAgg, price: UsagePrice | undefined, tr: ReturnType<typeof useT>): string {
  if (!agg.cost_est_usd) return tr("usage.cost_unpriced_hint");
  const lines = [tr("usage.cost_est_hint")];
  if (price) {
    lines.push(tr("usage.price_line", { in: fmtRate(price.in), out: fmtRate(price.out) }) + priceSrcLine(price));
    if (price.ambiguous) lines.push(tr("usage.price_ambiguous"));
  }
  if (agg.cost_usd) lines.push(tr("usage.cost_measured", { v: fmtUSD(agg.cost_usd) }));
  return lines.join("\n");
}

/** Metric-aware number formatting: tokens compact (fmtTok), counts grouped, cost in $. */
export function fmtMetric(metric: UsageMetric, v: number): string {
  if (metric === "cost_est_usd") return fmtUSDEst(v);
  if (metric === "cost_usd") return fmtUSD(v);
  if (metric === "calls") return fmtNum(Math.round(v));
  return fmtTok(Math.round(v));
}

export function UsageView() {
  const tr = useT();
  const wsState = useWorkspaceStore((s) => s.state);
  const running = wsState === "running";
  const startWs = useWorkspaceStore((s) => s.start);

  const [hours, setHours] = useState(24 * 7);
  const [by, setBy] = useState<UsageDim>("feature");
  const [metric, setMetric] = useState<UsageMetric>("spend");
  const [filters, setFilters] = useState<FilterTerm[]>([]);
  const [tableView, setTableView] = useState(false);
  const [matrixBy, setMatrixBy] = useState<"feature" | "kind">("feature");
  const [reloadTick, setReloadTick] = useState(0);

  const [series, setSeries] = useState<UsageSeries | null>(null);
  const [featModel, setFeatModel] = useState<UsageSeries | null>(null);
  const [kindModel, setKindModel] = useState<UsageSeries | null>(null);
  const [err, setErr] = useState("");
  const [loading, setLoading] = useState(true);
  // The sessions' own consumption is folded from the transcript into the ledger when it is read,
  // and asynchronously, so while folding runs the response omits the most recent turns
  // (docs/log/46 §3-b). The server declares that with folding, and this side refetches until it
  // settles; without that, users press reload repeatedly at a screen that never catches up.
  const [folding, setFolding] = useState(false);
  const [foldTick, setFoldTick] = useState(0);
  const foldTries = useRef(0);
  // Only an explicit refetch skips the throttle; on an automatic one folding would never stop.
  const forceFold = useRef(false);

  const filter = filterParam(filters);

  const load = useCallback(
    async (signal: AbortSignal): Promise<boolean> => {
      if (!running) return true; // no calls while the workspace is stopped; deps re-run on start
      // Pin now to the request time so the three series do not each see a different "now".
      const { from, to, bucket } = rangeOf(hours, new Date());
      const fold = forceFold.current ? ("force" as const) : undefined;
      const common = { from, to, filter: filter || undefined, fold };
      const [a, b, c] = await Promise.all([
        fetchUsageSeries({ ...common, bucket, by }, signal),
        fetchUsageSeries({ ...common, by: "feature", split: "model" }, signal),
        fetchUsageSeries({ ...common, by: "kind", split: "model" }, signal),
      ]);
      if (signal.aborted) return true;
      // A transient 502, which the CP returns right after the workspace starts, goes to retry.
      // Settling on empty data here would freeze the view at "no records" even once the agent is
      // up.
      if (isTransientErr(a) || isTransientErr(b) || isTransientErr(c)) return false;
      // From here the request is terminal, success or a real error, so force is spent. It is not
      // cleared on a retry: clearing it on a 502 would make the reload the user pressed hit the
      // throttle and do nothing.
      forceFold.current = false;
      const bad = [a, b, c].find((r) => (r as { error?: unknown })?.error);
      if (bad) {
        setErr(errText((bad as { error: { code?: string; message?: string } }).error));
        setFolding(false);
        setLoading(false);
        return true;
      }
      setErr("");
      setSeries(a as UsageSeries);
      setFeatModel(b as UsageSeries);
      setKindModel(c as UsageSeries);
      setFolding([a, b, c].some((r) => (r as UsageSeries).folding));
      setLoading(false);
      return true;
    },
    [running, hours, by, filter],
  );
  useRetryLoad(load, [running, hours, by, filter, reloadTick, foldTick]);

  // Automatic refetch until folding finishes. The cap exists so that a server which for any
  // reason fails to clear folding cannot turn this into endless polling; reload recovers from
  // where it stops.
  useEffect(() => {
    if (!folding) {
      foldTries.current = 0;
      return;
    }
    if (foldTries.current >= FOLD_POLL_MAX) return;
    const id = window.setTimeout(() => {
      foldTries.current++;
      setFoldTick((n) => n + 1);
    }, FOLD_POLL_MS);
    return () => window.clearTimeout(id);
  }, [folding, foldTick]);

  const reload = () => {
    forceFold.current = true;
    foldTries.current = 0;
    setReloadTick((n) => n + 1);
  };

  // The breakdowns come from the matrices, with no extra request: by feature = row totals, by
  // model = column totals, by agent = the row totals of kind x model.
  const featTotals = useMemo(() => matrixTotals(featModel, "row"), [featModel]);
  const modelTotals = useMemo(() => matrixTotals(featModel, "col"), [featModel]);
  const kindTotals = useMemo(() => matrixTotals(kindModel, "row"), [kindModel]);

  const stack = useMemo(
    () => stackModel(series?.buckets || [], by, metric),
    [series, by, metric],
  );
  const totals = series?.totals;
  const covers = useMemo(() => coverageNotes(series?.coverage), [series]);
  const notes = covers.filter((c) => !c.complete);

  const toggleFilter = (dim: string, value: string) => {
    setFilters((cur) =>
      cur.some((f) => f.dim === dim && f.value === value)
        ? cur.filter((f) => !(f.dim === dim && f.value === value))
        : [...cur, { dim, value }],
    );
  };
  const isFiltered = (dim: string, value: string) => filters.some((f) => f.dim === dim && f.value === value);

  // "other" is not an entity, so filtering on it expands to an OR over every folded real key
  // (series.ts).
  const pickSeries = (dim: string, value: string, folded: string[]) =>
    value === OTHER_KEY
      ? setFilters((cur) => toggleFoldedFilter(cur, dim, folded))
      : toggleFilter(dim, value);
  const seriesOn = (dim: string, value: string, folded: string[]) =>
    value === OTHER_KEY ? foldedFilterOn(filters, dim, folded) : isFiltered(dim, value);

  if (!running) {
    return (
      <div className="usage-board">
        <EmptyState icon="graph" title={tr("usage.ws_required_title")} hint={tr("usage.ws_required_hint")}>
          <Button icon="play" disabled={wsStartBusy(wsState)} onClick={() => void startWs()}>
            {wsStartBusy(wsState) ? tr("common.starting") : tr("usage.start_ws")}
          </Button>
        </EmptyState>
      </div>
    );
  }

  const empty = !loading && !err && (totals?.calls || 0) === 0 && (series?.unmeasured_calls || 0) === 0;

  return (
    <div className="usage-board">
      <p className="muted ds-note">{tr("usage.intro")}</p>

      {/* Filters live on one row and redraw every chart and table below from the same slice
          (dataviz: never a per-chart filter). */}
      <div className="usage-controls">
        <div className="uc-group" role="group" aria-label={tr("usage.range_label")}>
          {RANGES.map((r) => (
            <button
              key={r.hours}
              type="button"
              className={"uc-seg" + (hours === r.hours ? " active" : "")}
              aria-pressed={hours === r.hours}
              onClick={() => setHours(r.hours)}
            >
              {tr(r.key)}
            </button>
          ))}
        </div>
        <label className="uc-field">
          <span className="uc-lab muted">{tr("usage.by_label")}</span>
          <select value={by} onChange={(e) => setBy(e.target.value as UsageDim)}>
            {BY_DIMS.map((d) => (
              <option key={d.dim} value={d.dim}>
                {tr(d.key)}
              </option>
            ))}
          </select>
        </label>
        <label className="uc-field">
          <span className="uc-lab muted">{tr("usage.metric_label")}</span>
          <select value={metric} onChange={(e) => setMetric(e.target.value as UsageMetric)}>
            {METRICS.map((m) => (
              <option key={m.metric} value={m.metric}>
                {tr(m.key)}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="ghost uc-reload" onClick={reload}>
          <Icon name="refresh" /> {tr("usage.reload")}
        </button>
        {/* Say that folding is in progress. Showing stale numbers silently reads as "it never
            updates" and makes users hammer reload. */}
        {folding && (
          <span className="uc-folding muted" title={tr("usage.folding_hint")}>
            <Icon name="sync" spin /> {tr("usage.folding")}
          </span>
        )}
      </div>

      {!!filters.length && (
        <div className="usage-chips">
          <span className="muted">{tr("usage.filters")}</span>
          {filters.map((f) => (
            <button
              key={f.dim + ":" + f.value}
              type="button"
              className="usage-chip"
              onClick={() => toggleFilter(f.dim, f.value)}
              title={tr("usage.filter_remove")}
            >
              <span className="muted">{tr(("usage.by_" + f.dim) as MsgKey)}</span> {dimLabel(f.dim, f.value)}
              <Icon name="close" />
            </button>
          ))}
          <button type="button" className="ghost" onClick={() => setFilters([])}>
            {tr("usage.filter_clear")}
          </button>
        </div>
      )}

      {err && <p className="form-err">{err}</p>}

      {loading && !series ? (
        <p className="muted pad">{tr("common.loading")}</p>
      ) : empty ? (
        <EmptyState icon="graph" title={tr("usage.empty_title")} hint={tr("usage.empty_hint")} />
      ) : (
        <div className={"usage-stage" + (loading ? " reloading" : "")}>
          <KpiRow totals={totals} unmeasured={series?.unmeasured_calls || 0} />

          <section className="usage-card">
            <div className="uc-head">
              <h4>{tr("usage.chart_title", { by: tr(("usage.by_" + by) as MsgKey) })}</h4>
              <div className="uc-head-actions">
                {series?.truncated && (
                  <span className="usage-warn" title={tr("usage.truncated_hint")}>
                    <Icon name="warning" /> {tr("usage.truncated")}
                  </span>
                )}
                <button
                  type="button"
                  className={"uc-seg" + (tableView ? " active" : "")}
                  aria-pressed={tableView}
                  onClick={() => setTableView((v) => !v)}
                >
                  <Icon name="table" /> {tr("usage.table_view")}
                </button>
              </div>
            </div>
            {tableView ? (
              <SeriesTable stack={stack} by={by} metric={metric} bucket={series?.bucket || "day"} />
            ) : (
              <StackChart stack={stack} by={by} metric={metric} bucket={series?.bucket || "day"} />
            )}
            <Legend
              stack={stack}
              by={by}
              onPick={(k) => pickSeries(by, k, stack.foldedKeys)}
              isOn={(k) => seriesOn(by, k, stack.foldedKeys)}
            />
          </section>

          <div className="usage-breakdowns">
            <Breakdown
              title={tr("usage.breakdown_feature")}
              dim="feature"
              totals={featTotals}
              metric={metric}
              onPick={toggleFilter}
              isOn={isFiltered}
            />
            <Breakdown
              title={tr("usage.breakdown_kind")}
              dim="kind"
              totals={kindTotals}
              metric={metric}
              onPick={toggleFilter}
              isOn={isFiltered}
            />
            <Breakdown
              title={tr("usage.breakdown_model")}
              dim="model"
              totals={modelTotals}
              metric={metric}
              onPick={toggleFilter}
              isOn={isFiltered}
            />
          </div>

          <section className="usage-card">
            <div className="uc-head">
              <h4>{tr("usage.matrix_title")}</h4>
              <div className="uc-group" role="group" aria-label={tr("usage.matrix_title")}>
                <button
                  type="button"
                  className={"uc-seg" + (matrixBy === "feature" ? " active" : "")}
                  aria-pressed={matrixBy === "feature"}
                  onClick={() => setMatrixBy("feature")}
                >
                  {tr("usage.by_feature")}
                </button>
                <button
                  type="button"
                  className={"uc-seg" + (matrixBy === "kind" ? " active" : "")}
                  aria-pressed={matrixBy === "kind"}
                  onClick={() => setMatrixBy("kind")}
                >
                  {tr("usage.by_kind")}
                </button>
              </div>
            </div>
            <p className="muted uc-sub">{tr("usage.matrix_hint")}</p>
            <MatrixTable src={matrixBy === "feature" ? featModel : kindModel} rowDim={matrixBy} />
          </section>

          <CoverageBanner
            notes={notes}
            unmeasured={series?.unmeasured_calls || 0}
            priced={series?.priced_spend || 0}
            unpriced={series?.unpriced_spend || 0}
            catalog={series?.catalog}
          />
        </div>
      )}

      <RtkGainCard reloadTick={reloadTick} />
    </div>
  );
}

// How many buckets to show per grain; rtk returns the whole history, so only the tail is drawn.
const RTK_MODES = [
  { mode: "daily", n: 30, key: "usage.rtk_daily" },
  { mode: "weekly", n: 26, key: "usage.rtk_weekly" },
  { mode: "monthly", n: 24, key: "usage.rtk_monthly" },
] as const;
type RtkMode = (typeof RTK_MODES)[number]["mode"];

// rtk dates are bare "YYYY-MM-DD" / "YYYY-MM" with no timezone. Handing the string straight to
// Date treats it as UTC midnight, which shifts to the previous day or month in a negative-offset
// locale, so build it as a local date instead.
const rtkDate = (s: string): Date => {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y || 1970, (m || 1) - 1, d || 1);
};

/** Axis label for a bucket. `long` is for the tooltip and table row heading; monthly also shows
 * the year. */
const rtkBucketLabel = (b: RtkGainBucket, mode: RtkMode, long = false): string => {
  if (mode === "monthly")
    return fmtDateTime(rtkDate(b.month || ""), long ? { year: "numeric", month: "short" } : { month: "short" });
  const t = rtkDate((mode === "weekly" ? b.week_start : b.date) || "");
  return fmtDateTime(t, { month: "numeric", day: "numeric" });
};

/** Short duration display (ms to s to m to h). Not tokens, so not fmtTok. */
const fmtDur = (ms: number): string => {
  if (!isFinite(ms) || ms <= 0) return "—";
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60_000) return (ms / 1000).toFixed(1) + "s";
  if (ms < 3_600_000) return Math.round(ms / 60_000) + "m";
  return (ms / 3_600_000).toFixed(1) + "h";
};

// RtkGainCard — the rtk savings card ("rtk 効果"). A separate lineage from the ledger: an
// in-container measurement (see fetchRtkGain in api.ts) whose headline number is the all-time
// total. It does not follow the range presets or filters above; it switches rtk's own grains
// (daily / weekly / monthly) with its own segmented control, sharing only the reload button. It
// is the result side of the RTK toggle in Settings > Agents, and lives on the dashboard rather
// than in settings because monitoring is not configuration.
// A missing rtk, an error, or zero savings hides the whole card.
// Savings are positive and a single series, so they are painted in one accent colour rather than
// the resource warn/crit palette, and carry no legend (the title is the series name). The values
// are readable both in the tooltip and in the table view.
function RtkGainCard({ reloadTick }: { reloadTick: number }) {
  const tr = useT();
  const [gain, setGain] = useState<RtkGain | null>(null);
  const [mode, setMode] = useState<RtkMode>("daily");
  const [tableView, setTableView] = useState(false);
  const load = useCallback(async (signal: AbortSignal): Promise<boolean> => {
    const r = await fetchRtkGain(signal);
    if (signal.aborted) return true;
    if (isTransientErr(r)) return false; // a 502 right after workspace start goes to retryLoad
    setGain(r as RtkGain);
    return true;
  }, []);
  useRetryLoad(load, [reloadTick]);

  const s = gain?.summary;
  const saved = s?.total_saved || 0;
  if (!s || saved <= 0) return null;
  const pct = Math.round(s.avg_savings_pct || 0);
  // An older Agent returns only daily, so the control offers just the grains that have data.
  const modes = RTK_MODES.filter((m) => (gain?.[m.mode] || []).length > 0);
  const cur = modes.some((m) => m.mode === mode) ? mode : "daily";
  const curN = RTK_MODES.find((m) => m.mode === cur)?.n || 30;
  const buckets = (gain?.[cur] || []).slice(-curN);

  return (
    <section className="usage-card rtk-gain">
      <div className="uc-head">
        <h4>{tr("usage.rtk_gain_title")}</h4>
        <div className="uc-head-actions">
          {modes.length > 1 && (
            <div className="uc-group" role="group" aria-label={tr("usage.rtk_gain_title")}>
              {modes.map((m) => (
                <button
                  key={m.mode}
                  type="button"
                  className={"uc-seg" + (cur === m.mode ? " active" : "")}
                  aria-pressed={cur === m.mode}
                  onClick={() => setMode(m.mode)}
                >
                  {tr(m.key)}
                </button>
              ))}
            </div>
          )}
          <button
            type="button"
            className={"uc-seg" + (tableView ? " active" : "")}
            aria-pressed={tableView}
            onClick={() => setTableView((v) => !v)}
          >
            <Icon name="table" /> {tr("usage.table_view")}
          </button>
        </div>
      </div>
      <div className="rtk-gain-head">
        <div className="rtk-gain-headline">
          <b>{fmtTok(saved)}</b>
          <span className="muted">{tr("usage.rtk_cumulative")}</span>
        </div>
        <div className="rtk-gain-meter">
          <div className="wu-row-head">
            <span className="muted">{tr("usage.rtk_avg_pct")}</span>
            <span className="wu-pct">{pct}%</span>
          </div>
          <div className="wu-bar">
            <span className="wu-bar-fill" style={{ width: Math.min(100, pct) + "%" }} />
          </div>
        </div>
      </div>
      {tableView ? <RtkTable buckets={buckets} mode={cur} /> : <RtkChart buckets={buckets} mode={cur} />}
      <div className="rtk-stats">
        <div className="rtk-stat">
          <span className="muted">{tr("usage.rtk_in_out")}</span>
          <b>
            {fmtTok(s.total_input || 0)} → {fmtTok(s.total_output || 0)}
          </b>
        </div>
        <div className="rtk-stat">
          <span className="muted">{tr("usage.rtk_commands")}</span>
          <b>{fmtNum(s.total_commands || 0)}</b>
        </div>
        <div className="rtk-stat">
          <span className="muted">{tr("usage.rtk_time")}</span>
          <b>{tr("usage.rtk_time_avg", { total: fmtDur(s.total_time_ms || 0), avg: fmtDur(s.avg_time_ms || 0) })}</b>
        </div>
      </div>
      <p className="muted uc-sub rtk-note">{tr("usage.note_rtk_gain")}</p>
    </section>
  );
}

// The same .ux-* look, hover and label thinning as StackChart, redone thinly for a single series
// (saved tokens). It is not shared because the tooltips differ in shape: StackChart's breaks a
// bucket down by series, this one shows other metrics of the same bucket (savings rate, command
// count).
function RtkChart({ buckets, mode }: { buckets: RtkGainBucket[]; mode: RtkMode }) {
  const tr = useT();
  const [hover, setHover] = useState<number | null>(null);
  const top = niceMax(Math.max(...buckets.map((b) => b.saved_tokens || 0), 0));
  const accent = "var(--topbar-accent, var(--accent))";

  const plotRef = useRef<HTMLDivElement>(null);
  const [plotW, setPlotW] = useState(0);
  useEffect(() => {
    const el = plotRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => setPlotW(entries[0].contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const fitLabels = Math.max(1, Math.floor((plotW || 620) / (mode === "monthly" ? 52 : 40)));
  const stride = Math.max(1, Math.ceil(buckets.length / fitLabels));

  return (
    <div className="usage-chart">
      <div className="ux-yaxis" aria-hidden="true">
        <span>{fmtTok(top)}</span>
        <span>{fmtTok(top / 2)}</span>
        <span>0</span>
      </div>
      <div className="ux-plot" ref={plotRef}>
        <div className="ux-grid" aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
        <div className="ux-cols">
          {buckets.map((b, i) => (
            <button
              key={(b.date || b.week_start || b.month || "") + i}
              type="button"
              className={"ux-col" + (hover === i ? " on" : "")}
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover((h) => (h === i ? null : h))}
              onFocus={() => setHover(i)}
              onBlur={() => setHover((h) => (h === i ? null : h))}
              aria-label={`${rtkBucketLabel(b, mode, true)} ${fmtTok(b.saved_tokens || 0)}`}
            >
              <span className="ux-stack">
                <span
                  className="ux-seg topmost"
                  style={{
                    height: `calc(${(((b.saved_tokens || 0) / top) * 100).toFixed(3)}% - 2px)`,
                    background: accent,
                  }}
                />
              </span>
              {/* A thinned tick is an NBSP. An ordinary space collapses, the tick gets height 0,
                  and align-items:flex-end on .ux-cols drops the whole column below the baseline. */}
              <span className="ux-tick muted">{i % stride === 0 ? rtkBucketLabel(b, mode) : "\u00A0"}</span>
            </button>
          ))}
        </div>
        {hover != null && buckets[hover] && (
          <div
            className={"ux-tip" + (hover > buckets.length / 2 ? " right" : "")}
            style={{ left: `${((hover + 0.5) / Math.max(1, buckets.length)) * 100}%` }}
            role="status"
          >
            <div className="uxt-head">{rtkBucketLabel(buckets[hover], mode, true)}</div>
            <div className="uxt-row">
              <span className="uxt-key" style={{ background: accent }} />
              <span className="uxt-val">{fmtTok(buckets[hover].saved_tokens || 0)}</span>
              <span className="uxt-name muted">{tr("usage.rtk_saved")}</span>
            </div>
            <div className="uxt-row">
              <span className="uxt-key empty" />
              <span className="uxt-val">{Math.round(buckets[hover].savings_pct || 0)}%</span>
              <span className="uxt-name muted">{tr("usage.rtk_pct")}</span>
            </div>
            <div className="uxt-row">
              <span className="uxt-key empty" />
              <span className="uxt-val">{fmtNum(buckets[hover].commands || 0)}</span>
              <span className="uxt-name muted">{tr("usage.rtk_commands")}</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function RtkTable({ buckets, mode }: { buckets: RtkGainBucket[]; mode: RtkMode }) {
  const tr = useT();
  return (
    <div className="usage-table-wrap">
      <table className="usage-table">
        <thead>
          <tr>
            <th>{tr("usage.col_bucket")}</th>
            <th>{tr("usage.rtk_saved")}</th>
            <th>{tr("usage.rtk_pct")}</th>
            <th>{tr("usage.rtk_commands")}</th>
            <th>{tr("usage.rtk_time")}</th>
          </tr>
        </thead>
        <tbody>
          {buckets.map((b, i) => (
            <tr key={(b.date || b.week_start || b.month || "") + i}>
              <th scope="row">{rtkBucketLabel(b, mode, true)}</th>
              <td className="num strong">{fmtTok(b.saved_tokens || 0)}</td>
              <td className="num">{Math.round(b.savings_pct || 0)}%</td>
              <td className="num">{fmtNum(b.commands || 0)}</td>
              <td className="num">{fmtDur(b.total_time_ms || 0)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Row or column totals derived from a matrix, for the breakdown bars, so no extra request is
 * fired. */
function matrixTotals(s: UsageSeries | null, axis: "row" | "col"): Map<string, UsageAgg> {
  const m = new Map<string, UsageAgg>();
  const add = (k: string, a: UsageAgg) => {
    const cur = m.get(k);
    m.set(
      k,
      cur
        ? {
            spend: cur.spend + a.spend,
            in: cur.in + a.in,
            out: cur.out + a.out,
            cread: cur.cread + a.cread,
            ccreate: cur.ccreate + a.ccreate,
            calls: cur.calls + a.calls,
            cost_usd: (cur.cost_usd || 0) + (a.cost_usd || 0),
          }
        : { ...a },
    );
  };
  for (const [rowKey, cols] of Object.entries(s?.matrix || {})) {
    for (const [colKey, agg] of Object.entries(cols)) add(axis === "row" ? rowKey : colKey, agg);
  }
  return m;
}

// --- KPI ---------------------------------------------------------------------

// The primary metric spend is large, with cache_read and cost beside it. No second-axis chart:
// dataviz forbids dual axes because they fabricate correlation. Unmeasured gets its own tile,
// positioned where it cannot blend into a zero.
function KpiRow({ totals, unmeasured }: { totals: UsageAgg | undefined; unmeasured: number }) {
  const tr = useT();
  const t = totals;
  // The measured value is kept alongside, never added: it is the only check on the estimate.
  const measured = t?.cost_usd || 0;
  const costTitle =
    tr("usage.kpi_cost_hint") + (measured > 0 ? "\n" + tr("usage.cost_measured", { v: fmtUSD(measured) }) : "");
  return (
    <div className="usage-kpis">
      <div className="ukpi hero">
        <div className="ukpi-val">{fmtTok(t?.spend || 0)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_spend")}</div>
      </div>
      <div className="ukpi">
        <div className="ukpi-val">{fmtNum(t?.calls || 0)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_calls")}</div>
      </div>
      <div className="ukpi">
        <div className="ukpi-val">{fmtTok(t?.cread || 0)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_cread")}</div>
      </div>
      <div className="ukpi" title={costTitle}>
        <div className="ukpi-val">{fmtUSDEst(t?.cost_est_usd || 0)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_cost")}</div>
      </div>
      <div className={"ukpi" + (unmeasured > 0 ? " unmeasured" : "")} title={tr("usage.kpi_unmeasured_hint")}>
        <div className="ukpi-val">{fmtNum(unmeasured)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_unmeasured")}</div>
      </div>
    </div>
  );
}

// --- Stacked bars (time series) ----------------------------------------------

interface ChartProps {
  stack: ReturnType<typeof stackModel>;
  by: string;
  metric: UsageMetric;
  bucket: string;
}

const bucketLabel = (t: string, bucket: string): string =>
  bucket === "hour" ? fmtDateTime(t, TIME_HM) : fmtDateTime(t, { month: "numeric", day: "numeric" });

// Only three gridlines: 0, midpoint and top (1px solid, one step off the background). The top is
// rounded to a nice number, but on a fine ladder (1, 2, 2.5, 3, 4, 5, 8, 10). A coarse ladder
// jumps a 3.3M maximum to 5M, leaving the bars only half the height and hiding day-to-day
// differences. Half the top is also a gridline, so only numbers that halve cleanly are listed.
function niceMax(v: number): number {
  if (v <= 0) return 1;
  const pow = Math.pow(10, Math.floor(Math.log10(v)));
  const n = v / pow;
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 3 ? 3 : n <= 4 ? 4 : n <= 5 ? 5 : n <= 8 ? 8 : 10;
  return step * pow;
}

function StackChart({ stack, by, metric, bucket }: ChartProps) {
  const tr = useT();
  const [hover, setHover] = useState<number | null>(null);
  const top = niceMax(stack.max);
  const rows = stack.rows;

  // Thinning of the tick labels. At a narrow width (phone, pane) writing all 11 or 24 dates
  // overlaps them into illegibility, so the width is measured and every nth label is drawn. Every
  // bar is still drawn, and the values stay readable in the tooltip and the table view, so
  // thinning labels hides no data.
  const plotRef = useRef<HTMLDivElement>(null);
  const [plotW, setPlotW] = useState(0);
  useEffect(() => {
    const el = plotRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver((entries) => setPlotW(entries[0].contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  const fitLabels = Math.max(1, Math.floor((plotW || 620) / (bucket === "hour" ? 44 : 40)));
  const stride = Math.max(1, Math.ceil(rows.length / fitLabels));

  return (
    <div className="usage-chart">
      <div className="ux-yaxis" aria-hidden="true">
        <span>{fmtMetric(metric, top)}</span>
        <span>{fmtMetric(metric, top / 2)}</span>
        <span>0</span>
      </div>
      <div className="ux-plot" ref={plotRef}>
        <div className="ux-grid" aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
        <div className="ux-cols">
          {rows.map((row, i) => (
            <button
              key={row.t}
              type="button"
              className={"ux-col" + (hover === i ? " on" : "")}
              // The tooltip is a supplement; the same values are in the table view, so nothing
              // depends on colour or hover alone.
              onMouseEnter={() => setHover(i)}
              onMouseLeave={() => setHover((h) => (h === i ? null : h))}
              onFocus={() => setHover(i)}
              onBlur={() => setHover((h) => (h === i ? null : h))}
              aria-label={`${bucketLabel(row.t, bucket)} ${fmtMetric(metric, row.total)}`}
            >
              <span className="ux-stack">
                {row.segs.map((s, si) => (
                  <span
                    key={s.key}
                    className={"ux-seg" + (si === row.segs.length - 1 ? " topmost" : "")}
                    style={{
                      height: `calc(${((s.value / top) * 100).toFixed(3)}% - 2px)`,
                      background: s.color,
                    }}
                  />
                ))}
              </span>
              <span className="ux-tick muted">{i % stride === 0 ? bucketLabel(row.t, bucket) : "\u00A0"}</span>
            </button>
          ))}
        </div>
        {hover != null && rows[hover] && (
          <div
            className={"ux-tip" + (hover > rows.length / 2 ? " right" : "")}
            style={{ left: `${((hover + 0.5) / Math.max(1, rows.length)) * 100}%` }}
            role="status"
          >
            <div className="uxt-head">{bucketLabel(rows[hover].t, bucket)}</div>
            {rows[hover].segs
              .slice()
              .reverse()
              .map((s) => (
                <div className="uxt-row" key={s.key}>
                  <span className="uxt-key" style={{ background: s.color }} />
                  <span className="uxt-val">{fmtMetric(metric, s.value)}</span>
                  <span className="uxt-name muted">{dimLabel(by, s.key)}</span>
                </div>
              ))}
            <div className="uxt-row total">
              <span className="uxt-key empty" />
              <span className="uxt-val">{fmtMetric(metric, rows[hover].total)}</span>
              <span className="uxt-name muted">{tr("usage.total")}</span>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

// The legend appears with two or more series, or whenever anything is folded, so identity is
// never carried by colour alone. Hiding it while something is folded leaves an unnamed grey bar
// and no way to tell what was consumed. Clicking filters to that series; "other" filters on an OR
// over every folded real key.
export function Legend({
  stack,
  by,
  onPick,
  isOn,
}: {
  stack: ReturnType<typeof stackModel>;
  by: string;
  onPick: (key: string) => void;
  isOn: (key: string) => boolean;
}) {
  const tr = useT();
  if (stack.legend.length < 2 && !stack.foldedKeys.length) return null;
  return (
    <div className="usage-legend">
      {stack.legend.map((l) => (
        <button
          key={l.key}
          type="button"
          className={"ulg" + (isOn(l.key) ? " on" : "")}
          onClick={() => onPick(l.key)}
          title={
            l.key === OTHER_KEY
              ? stack.foldedKeys.map((k) => dimLabel(by, k)).join(", ") + " — " + tr("usage.filter_add")
              : tr("usage.filter_add")
          }
        >
          <span className="ulg-sw" style={{ background: l.color }} />
          {dimLabel(by, l.key)}
        </button>
      ))}
    </div>
  );
}

// Table view: the WCAG twin of the chart, giving the same values without colour, so no value is
// locked inside a tooltip.
function SeriesTable({ stack, by, metric, bucket }: ChartProps) {
  const tr = useT();
  const keys = stack.legend.map((l) => l.key);
  return (
    <div className="usage-table-wrap">
      <table className="usage-table">
        <thead>
          <tr>
            <th>{tr("usage.col_bucket")}</th>
            {keys.map((k) => (
              <th key={k}>{dimLabel(by, k)}</th>
            ))}
            <th>{tr("usage.total")}</th>
          </tr>
        </thead>
        <tbody>
          {stack.rows.map((row) => (
            <tr key={row.t}>
              <th scope="row">{bucketLabel(row.t, bucket)}</th>
              {keys.map((k) => {
                const seg = row.segs.find((s) => s.key === k);
                return (
                  <td key={k} className="num">
                    {seg ? fmtMetric(metric, seg.value) : "—"}
                  </td>
                );
              })}
              <td className="num strong">{fmtMetric(metric, row.total)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// --- Breakdown (horizontal bars) ---------------------------------------------

function Breakdown({
  title,
  dim,
  totals,
  metric,
  onPick,
  isOn,
}: {
  title: string;
  dim: string;
  totals: Map<string, UsageAgg>;
  metric: UsageMetric;
  onPick: (dim: string, key: string) => void;
  isOn: (dim: string, key: string) => boolean;
}) {
  const tr = useT();
  const rows = useMemo(() => breakdownRows(totals, dim, metric), [totals, dim, metric]);
  return (
    <section className="usage-card ubd">
      <div className="uc-head">
        <h4>{title}</h4>
      </div>
      {rows.length === 0 ? (
        <p className="muted">{tr("usage.no_rows")}</p>
      ) : (
        <div className="ubd-rows">
          {rows.map((r) => (
            <button
              key={r.key}
              type="button"
              className={"ubd-row" + (isOn(dim, r.key) ? " on" : "")}
              onClick={() => onPick(dim, r.key)}
              title={tr("usage.calls_n", { n: fmtNum(r.agg.calls) })}
            >
              <span className="ubd-name">{dimLabel(dim, r.key)}</span>
              <span className="ubd-bar">
                <span
                  className="ubd-fill"
                  style={{ width: `${(r.frac * 100).toFixed(2)}%`, background: r.color }}
                />
              </span>
              {/* Label the value directly; this also relieves the weak contrast of the light
                  slots. */}
              <span className="ubd-val">{fmtMetric(metric, r.value)}</span>
            </button>
          ))}
        </div>
      )}
    </section>
  );
}

// --- The feature x model table -----------------------------------------------

// The view docs/log/46 §2-b was really after: which model each feature runs on. If auxiliary
// calls are going to the CLI's default flagship, it shows here in the call count and the
// average.
function MatrixTable({ src, rowDim }: { src: UsageSeries | null; rowDim: string }) {
  const tr = useT();
  const rows = useMemo(() => matrixRows(src?.matrix), [src]);
  if (!rows.length) return <p className="muted">{tr("usage.no_rows")}</p>;
  return (
    <div className="usage-table-wrap">
      <table className="usage-table">
        <thead>
          <tr>
            <th>{tr(("usage.by_" + rowDim) as MsgKey)}</th>
            <th>{tr("usage.by_model")}</th>
            <th className="num">{tr("usage.col_calls")}</th>
            <th className="num">{tr("usage.col_spend")}</th>
            <th className="num">{tr("usage.col_avg")}</th>
            <th className="num">{tr("usage.col_cost")}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) =>
            row.cells.map((c, i) => (
              <tr key={row.key + "|" + c.key}>
                {i === 0 && (
                  <th scope="row" rowSpan={row.cells.length}>
                    {dimLabel(rowDim, row.key)}
                  </th>
                )}
                <td>{dimLabel("model", c.key)}</td>
                {/* When one call splits across models, the count lands on the single row of the
                    model that consumed most (aggregateUsageRows, server side). A row with 0 calls
                    but non-zero spend is its counterpart, so per-call shows "—" rather than 0,
                    with the reason in the tooltip. */}
                <td className="num" title={c.agg.calls === 0 && c.agg.spend > 0 ? tr("usage.calls_shared") : undefined}>
                  {fmtNum(c.agg.calls)}
                </td>
                <td className="num">{fmtTok(c.agg.spend)}</td>
                <td className="num">{c.agg.calls > 0 ? fmtTok(Math.round(perCall(c.agg))) : "—"}</td>
                {/* The estimate. A model missing from the price table shows "—" and not 0, with
                    the reason in the tooltip, so zero is never confused with unpriceable. The
                    unit price, its source and any measured value go in the same tooltip. */}
                <td className="num" title={costCellTitle(c.agg, src?.prices?.[c.key], tr)}>
                  {fmtUSDEst(c.agg.cost_est_usd || 0)}
                </td>
              </tr>
            )),
          )}
        </tbody>
      </table>
    </div>
  );
}

// --- Coverage banner ---------------------------------------------------------

// The server derives coverage from the observed data. Never hand-write the wording here: a
// hand-written table drifts the moment another agent is added.
function CoverageBanner({
  notes,
  unmeasured,
  priced,
  unpriced,
  catalog,
}: {
  notes: ReturnType<typeof coverageNotes>;
  unmeasured: number;
  priced: number;
  unpriced: number;
  catalog: UsageCatalog | undefined;
}) {
  const tr = useT();
  // How much of the consumption the estimate could be derived from. Staying silent about the
  // unpriceable part makes "≈$41" read as the amount for everything.
  const unpricedPct = priced + unpriced > 0 ? Math.round((unpriced / (priced + unpriced)) * 100) : 0;
  if (!notes.length && unmeasured === 0 && unpriced === 0 && !catalog) return null;
  return (
    <section className="usage-card usage-coverage">
      <div className="uc-head">
        <h4>
          <Icon name="info" /> {tr("usage.coverage_title")}
        </h4>
      </div>
      {unmeasured > 0 && <p className="uc-sub">{tr("usage.coverage_unmeasured", { n: fmtNum(unmeasured) })}</p>}
      {/* A fraction that rounds to 0% would give the self-contradictory "0% of consumption is
          ...", so anything below 1% gets its own wording (seen on real data: 57k / 34.1M). */}
      {unpriced > 0 && (
        <p className="uc-sub">
          {unpricedPct >= 1
            ? tr("usage.coverage_unpriced", { pct: String(unpricedPct), n: fmtTok(unpriced) })
            : tr("usage.coverage_unpriced_sub1", { n: fmtTok(unpriced) })}
        </p>
      )}
      {/* Always show when the catalogue was fetched: estimates are not stored, so a catalogue
          update changes past amounts too, and moving the amounts without saying which prices
          they came from is close to lying silently. */}
      {catalog && (
        <p className="uc-sub" title={tMaybe("usage.catalog_origin_" + catalog.origin) ?? undefined}>
          {tr("usage.catalog_note", {
            n: fmtNum(catalog.models),
            when: catalog.fetched ? fmtDateTime(catalog.fetched, { month: "numeric", day: "numeric" }) : "—",
          })}
        </p>
      )}
      <ul className="ucov-list">
        {notes.map((n) => (
          <li key={n.kind}>
            <span className={"kind-tag kind-" + n.kind}>{n.kind}</span>
            <span className="muted">
              {tMaybe("usage.cov_tokens_" + n.tokens) ?? n.tokens} /{" "}
              {tMaybe("usage.cov_model_" + n.model) ?? n.model}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
