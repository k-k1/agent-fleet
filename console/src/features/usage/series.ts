// features/usage/series — /usage/series の応答 → 描画に必要な形（純関数・テスト対象）。
//
// UsageView は「描く」だけに集中させ、集計の畳み込み・順序付け・スケールはここに寄せる。
// サーバは既に集計済みなので、ここでやるのは *表示のための* 整形（系列の合計・スロット
// 順の積み上げ・畳み込み）だけ。
// api.ts からは **型だけ** を取る（型 import はビルド時に消える）。値を1つでも取ると
// core/api/client.ts が読み込まれ、モジュール初期化で localStorage を触るため node 環境の
// ユニットテストが落ちる — この層を純粋に保つのはテスト可能性のための設計。
import type { UsageAgg, UsageBucket, UsageCoverage, UsageSeries } from "./api.ts";
import { MAX_SLOTS, OTHER_KEY, paintSeries, slotColor } from "./colors.ts";
import type { SeriesPaint } from "./colors.ts";

export const EMPTY_AGG: UsageAgg = { spend: 0, in: 0, out: 0, cread: 0, ccreate: 0, calls: 0 };

/** グラフに出す指標。spend が主指標（cache_read を含まない既存定義と揃える）。 */
export type UsageMetric = "spend" | "calls" | "cread" | "cost_usd" | "cost_est_usd";

export const metricOf = (a: UsageAgg | undefined, m: UsageMetric): number => {
  if (!a) return 0;
  const v = m === "cost_usd" ? a.cost_usd || 0 : m === "cost_est_usd" ? a.cost_est_usd || 0 : a[m];
  return typeof v === "number" && isFinite(v) ? v : 0;
};

/** 金額の指標か（表示に $ と「≈」を付けるかの判定を1か所に持つ）。 */
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

/** 期間全体での系列ごとの合計（凡例の並び順・内訳バーの元）。 */
export function totalsByKey(buckets: UsageBucket[]): Map<string, UsageAgg> {
  const m = new Map<string, UsageAgg>();
  for (const b of buckets) {
    for (const [k, agg] of Object.entries(b.series || {})) {
      m.set(k, addAgg(m.get(k) || { ...EMPTY_AGG }, agg));
    }
  }
  return m;
}

/** 量の多い順のキー配列（畳み込み対象を決めるためだけに使う。色は順位に依存しない）。 */
export function keysByMagnitude(totals: Map<string, UsageAgg>, metric: UsageMetric): string[] {
  return [...totals.entries()]
    .sort((a, b) => metricOf(b[1], metric) - metricOf(a[1], metric) || (a[0] < b[0] ? -1 : 1))
    .map(([k]) => k);
}

export interface StackSeg {
  key: string;
  color: string;
  value: number;
  /** 0..1 のバケット内比率（積み上げの高さ）。 */
  frac: number;
}
export interface StackRow {
  t: string;
  total: number;
  segs: StackSeg[];
}
export interface StackModel {
  rows: StackRow[];
  /** 縦軸の上端（バケット合計の最大）。0 なら全部空。 */
  max: number;
  /** 凡例（スロット順・畳み込み済み）。 */
  legend: SeriesPaint[];
  /** その他へ畳まれた実キー（凡例の注記・表で名前を出すため）。 */
  foldedKeys: string[];
}

/**
 * stackModel — バケット列を「スロット順に積んだ棒」に変換する。
 *
 * 畳み込み: 上位 MAX_SLOTS 件だけが自分の色を持ち、残りは OTHER_KEY にまとめる。
 * 積み順は必ずスロット順（colors.ts の規約2）。
 */
export function stackModel(buckets: UsageBucket[], dim: string, metric: UsageMetric): StackModel {
  const totals = totalsByKey(buckets);
  const ordered = keysByMagnitude(totals, metric);
  const painted = paintSeries(dim, ordered);
  const paintByKey = new Map(painted.map((p) => [p.key, p]));
  const foldedKeys = painted.filter((p) => p.folded).map((p) => p.key);

  // 凡例＝スロットを持つ系列（スロット順）＋ 畳み込みがあれば「その他」1件。
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
  /** 最大値に対する比（横棒の長さ）。 */
  frac: number;
  /** 全体に対する比（%表示）。 */
  share: number;
}

/**
 * breakdownRows — 期間合計の内訳（横棒）。**量の多い順**に並べるが、色は
 * paintSeries が決めた実体固定の色をそのまま使う（積み上げ棒と同じ色＝読み手が
 * 2つのグラフを行き来できる）。畳まれた系列も行としては個別に出す（グレー）。
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

/** matrix（by × split）を「行＝by・列＝split」の表に整形。行は spend 降順。 */
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

/** 1呼び出しあたりの平均（表の「平均」列）。calls 0 は 0 とする。 */
export const perCall = (agg: UsageAgg, m: UsageMetric = "spend"): number =>
  agg.calls > 0 ? metricOf(agg, m) / agg.calls : 0;

export interface CoverageNote {
  kind: string;
  tokens: string;
  model: string;
  /** 完全（トークン実測＋モデル報告）なら注記は要らない。 */
  complete: boolean;
}

/**
 * coverageNotes — 未計測バナーの元。**データから起こす**（手書きの表はドリフトする）。
 * 「トークンが exact かつモデルが reported」以外は全部注記の対象。
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

/** hour バケットを使うべき期間か（24時間以内は時間粒度が読みやすい）。 */
export const bucketFor = (rangeHours: number): "day" | "hour" => (rangeHours <= 24 ? "hour" : "day");

/** 期間プリセット → from/to（ISO 文字列）。now はテストのため注入する。 */
export function rangeOf(hours: number, now: Date): { from: string; to: string; bucket: "day" | "hour" } {
  const to = new Date(now.getTime());
  const from = new Date(now.getTime() - hours * 3600 * 1000);
  return { from: from.toISOString(), to: to.toISOString(), bucket: bucketFor(hours) };
}

export const seriesIsEmpty = (s: UsageSeries | null): boolean =>
  !s || !s.buckets?.length || (s.totals?.calls || 0) === 0;

/** 絞り込み1件（軸 × 値）。API へは `dim:value` のカンマ連結で渡す（同一軸は OR）。 */
export interface FilterTerm {
  dim: string;
  value: string;
}

export const filterParam = (terms: FilterTerm[]): string => terms.map((f) => `${f.dim}:${f.value}`).join(",");

/**
 * 「その他」の絞り込みトグル。**畳まれた実キー全部の OR に展開する**（同一軸は OR）。
 *
 * その他は実体ではないので、そのままでは絞り込めない = グレーの棒だけ中身を確かめる手段が
 * 無くなる（色スロットを持たない feature が1つ出た時にまさにこれが起きていた）。全部
 * 選択済みなら解除、そうでなければ全部立てる。
 */
export function toggleFoldedFilter(cur: FilterTerm[], dim: string, keys: string[]): FilterTerm[] {
  if (!keys.length) return cur;
  const without = cur.filter((f) => !(f.dim === dim && keys.includes(f.value)));
  return foldedFilterOn(cur, dim, keys) ? without : [...without, ...keys.map((value) => ({ dim, value }))];
}

/** 畳まれたキーが全部絞り込まれているか（＝「その他」が選択状態か）。 */
export const foldedFilterOn = (cur: FilterTerm[], dim: string, keys: string[]): boolean =>
  keys.length > 0 && keys.every((k) => cur.some((f) => f.dim === dim && f.value === k));

export { MAX_SLOTS, OTHER_KEY };
