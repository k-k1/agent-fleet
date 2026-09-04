// features/usage/series — turns the /usage/series response into the shape the chart needs
// (pure functions, unit-testable).
//
// UsageView only draws; folding, ordering and scaling live here. The server has already
// aggregated, so all that happens here is presentation shaping (series totals, stacking in
// slot order, folding).
// Import **types only** from api.ts (type imports vanish at build time). Pulling in a single
// value drags in core/api/client.ts, which touches localStorage at module init and breaks the
// unit tests under node — keeping this layer pure is what makes it testable.
import type { UsageAgg, UsageBucket, UsageCoverage, UsageSeries } from "./api.ts";
import { MAX_SLOTS, OTHER_KEY, paintSeries, slotColor } from "./colors.ts";
import type { SeriesPaint } from "./colors.ts";

export const EMPTY_AGG: UsageAgg = { spend: 0, in: 0, out: 0, cread: 0, ccreate: 0, calls: 0 };

/** Metric plotted on the chart. spend is the primary one (matching the existing definition,
 *  which excludes cache_read). */
export type UsageMetric = "spend" | "calls" | "cread" | "cost_usd" | "cost_est_usd";

export const metricOf = (a: UsageAgg | undefined, m: UsageMetric): number => {
  if (!a) return 0;
  const v = m === "cost_usd" ? a.cost_usd || 0 : m === "cost_est_usd" ? a.cost_est_usd || 0 : a[m];
  return typeof v === "number" && isFinite(v) ? v : 0;
};

/** Whether the metric is money — the single place that decides if $ and "≈" are shown. */
export const isMoneyMetric = (m: UsageMetric): boolean => m === "cost_usd" || m === "cost_est_usd";

export function addAgg(a: UsageAgg, b: UsageAgg | undefined): UsageAgg {
  if (!b) return a;
  return {
    spend: a.spend + b.spend,
    in: a.in + b.in,
    out: a.out + b.out,
    cread: a.cread + b.cread,
    ccreate: a.ccreate + b.ccreate,
    calls: a.calls + b.calls,
    cost_usd: (a.cost_usd || 0) + (b.cost_usd || 0),
    cost_est_usd: (a.cost_est_usd || 0) + (b.cost_est_usd || 0),
  };
}

/** Per-series totals over the whole range (source of the legend order and the breakdown bars). */
export function totalsByKey(buckets: UsageBucket[]): Map<string, UsageAgg> {
  const m = new Map<string, UsageAgg>();
  for (const b of buckets) {
    for (const [k, agg] of Object.entries(b.series || {})) {
      m.set(k, addAgg(m.get(k) || { ...EMPTY_AGG }, agg));
    }
  }
  return m;
}

/** Keys ordered by magnitude, used only to decide what gets folded — colour does not depend
 *  on rank. */
export function keysByMagnitude(totals: Map<string, UsageAgg>, metric: UsageMetric): string[] {
  return [...totals.entries()]
    .sort((a, b) => metricOf(b[1], metric) - metricOf(a[1], metric) || (a[0] < b[0] ? -1 : 1))
    .map(([k]) => k);
}

export interface StackSeg {
  key: string;
  color: string;
  value: number;
  /** 0..1 share within the bucket (the stacked height). */
  frac: number;
}
export interface StackRow {
  t: string;
  total: number;
  segs: StackSeg[];
}
export interface StackModel {
  rows: StackRow[];
  /** Top of the y axis (largest bucket total). 0 means everything is empty. */
  max: number;
  /** Legend, in slot order, already folded. */
  legend: SeriesPaint[];
  /** The real keys folded into "other", so the legend note and the table can name them. */
  foldedKeys: string[];
}

/**
 * stackModel — turns a list of buckets into bars stacked in slot order.
 *
 * Folding: only the top MAX_SLOTS series keep their own colour; the rest collapse into
 * OTHER_KEY. The stacking order is always slot order (rule 2 in colors.ts).
 */
export function stackModel(buckets: UsageBucket[], dim: string, metric: UsageMetric): StackModel {
  const totals = totalsByKey(buckets);
  const ordered = keysByMagnitude(totals, metric);
  const painted = paintSeries(dim, ordered);
  const paintByKey = new Map(painted.map((p) => [p.key, p]));
  const foldedKeys = painted.filter((p) => p.folded).map((p) => p.key);

  // Legend = the series that hold a slot, in slot order, plus one "other" entry if anything folded.
  const legend: SeriesPaint[] = painted.filter((p) => !p.folded);
  if (foldedKeys.length) legend.push({ key: OTHER_KEY, slot: 0, color: slotColor(0), folded: true });

  const rows: StackRow[] = [];
  let max = 0;
  for (const b of buckets) {
    const bySlot = new Map<string, number>();
    for (const [k, agg] of Object.entries(b.series || {})) {
      const p = paintByKey.get(k);
      const key = !p || p.folded ? OTHER_KEY : k;
      bySlot.set(key, (bySlot.get(key) || 0) + metricOf(agg, metric));
    }
    const segs: StackSeg[] = [];
    let total = 0;
    for (const l of legend) {
      const v = bySlot.get(l.key) || 0;
      if (v <= 0) continue;
      segs.push({ key: l.key, color: l.color, value: v, frac: 0 });
      total += v;
    }
    for (const s of segs) s.frac = total > 0 ? s.value / total : 0;
    rows.push({ t: b.t, total, segs });
    if (total > max) max = total;
  }
  return { rows, max, legend, foldedKeys };
}

export interface BreakdownRow {
  key: string;
  color: string;
  folded: boolean;
  agg: UsageAgg;
  value: number;
  /** Share of the largest value (the length of the horizontal bar). */
  frac: number;
  /** Share of the whole (shown as a percentage). */
  share: number;
}

/**
 * breakdownRows — the range total broken down into horizontal bars. Rows are ordered by
 * magnitude, but the colour is the entity-fixed one paintSeries chose, unchanged: the same
 * colour as in the stacked bars, so a reader can move between the two charts. Folded series
 * still get their own row, in grey.
 */
export function breakdownRows(totals: Map<string, UsageAgg>, dim: string, metric: UsageMetric): BreakdownRow[] {
  const ordered = keysByMagnitude(totals, metric);
  const paint = new Map(paintSeries(dim, ordered).map((p) => [p.key, p]));
  let max = 0;
  let sum = 0;
  for (const k of ordered) {
    const v = metricOf(totals.get(k), metric);
    if (v > max) max = v;
    sum += v;
  }
  return ordered.map((key) => {
    const agg = totals.get(key) || { ...EMPTY_AGG };
    const value = metricOf(agg, metric);
    const p = paint.get(key);
    return {
      key,
      color: p ? p.color : slotColor(0),
      folded: !!p?.folded,
      agg,
      value,
      frac: max > 0 ? value / max : 0,
      share: sum > 0 ? value / sum : 0,
    };
  });
}

/** Reshapes matrix (by x split) into a table of rows = by, columns = split, rows sorted by
 *  descending spend. */
export interface MatrixCell {
  key: string;
  agg: UsageAgg;
}
export interface MatrixRow {
  key: string;
  total: UsageAgg;
  cells: MatrixCell[];
}

export function matrixRows(matrix: Record<string, Record<string, UsageAgg>> | undefined): MatrixRow[] {
  if (!matrix) return [];
  const rows: MatrixRow[] = [];
  for (const [key, cols] of Object.entries(matrix)) {
    let total: UsageAgg = { ...EMPTY_AGG };
    const cells: MatrixCell[] = [];
    for (const [ck, agg] of Object.entries(cols)) {
      cells.push({ key: ck, agg });
      total = addAgg(total, agg);
    }
    cells.sort((a, b) => b.agg.spend - a.agg.spend || (a.key < b.key ? -1 : 1));
    rows.push({ key, total, cells });
  }
  rows.sort((a, b) => b.total.spend - a.total.spend || (a.key < b.key ? -1 : 1));
  return rows;
}

/** Average per call (the table's average column). calls = 0 yields 0. */
export const perCall = (agg: UsageAgg, m: UsageMetric = "spend"): number =>
  agg.calls > 0 ? metricOf(agg, m) / agg.calls : 0;

export interface CoverageNote {
  kind: string;
  tokens: string;
  model: string;
  /** Complete (tokens measured and model reported) needs no note. */
  complete: boolean;
}

/**
 * coverageNotes — source of the "not measured" banner. Derived from the data, because a
 * hand-written table drifts. Anything other than tokens = exact and model = reported gets a note.
 */
export function coverageNotes(coverage: Record<string, UsageCoverage> | undefined): CoverageNote[] {
  const out: CoverageNote[] = [];
  for (const [kind, c] of Object.entries(coverage || {})) {
    if (!kind) continue;
    const tokens = c?.tokens || "none";
    const model = c?.model || "none";
    out.push({ kind, tokens, model, complete: tokens === "exact" && model === "reported" });
  }
  return out.sort((a, b) => (a.kind < b.kind ? -1 : 1));
}

/** Whether the range wants hour buckets — up to 24h, hourly granularity reads better. */
export const bucketFor = (rangeHours: number): "day" | "hour" => (rangeHours <= 24 ? "hour" : "day");

/** Range preset to from/to as ISO strings. now is injected so tests can control it. */
export function rangeOf(hours: number, now: Date): { from: string; to: string; bucket: "day" | "hour" } {
  const to = new Date(now.getTime());
  const from = new Date(now.getTime() - hours * 3600 * 1000);
  return { from: from.toISOString(), to: to.toISOString(), bucket: bucketFor(hours) };
}

export const seriesIsEmpty = (s: UsageSeries | null): boolean =>
  !s || !s.buckets?.length || (s.totals?.calls || 0) === 0;

/** One filter term (dimension x value). Passed to the API as comma-joined `dim:value`; terms
 *  on the same dimension are OR-ed. */
export interface FilterTerm {
  dim: string;
  value: string;
}

export const filterParam = (terms: FilterTerm[]): string => terms.map((f) => `${f.dim}:${f.value}`).join(",");

/**
 * Filter toggle for "other". Expands to an OR over every folded key (same dimension = OR).
 *
 * "Other" is not an entity, so it cannot be filtered as itself, and without this the grey bar
 * would be the one bar whose contents cannot be inspected. If all keys are already selected,
 * clear them; otherwise set them all.
 */
export function toggleFoldedFilter(cur: FilterTerm[], dim: string, keys: string[]): FilterTerm[] {
  if (!keys.length) return cur;
  const without = cur.filter((f) => !(f.dim === dim && keys.includes(f.value)));
  return foldedFilterOn(cur, dim, keys) ? without : [...without, ...keys.map((value) => ({ dim, value }))];
}

/** Whether every folded key is filtered, i.e. whether "other" reads as selected. */
export const foldedFilterOn = (cur: FilterTerm[], dim: string, keys: string[]): boolean =>
  keys.length > 0 && keys.every((k) => cur.some((f) => f.dim === dim && f.value === k));

export { MAX_SLOTS, OTHER_KEY };
