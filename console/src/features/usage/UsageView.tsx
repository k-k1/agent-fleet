// UsageView — 機能別トークン使用量のダッシュボード（docs/46 P4 / ADR0029 §7-5）。
//
// **モーダル非依存の純粋コンポーネント**として書く。今は設定モーダルの「使用量」タブが
// 薄いラッパとして差しているだけで、将来ペインに昇格させたくなったら PaneKind を足して
// 同じ View を差せる（逆はできない＝ペイン前提で書くと設定に入らないので、この順序）。
//
// 読むもの: GET /api/usage/series（サーバ側で集計済み。生ログは流れてこない）。
// 1画面で3リクエスト — 選択中の軸の時系列 / 機能×モデル / エージェント×モデル。後ろ2つは
// matrix なので、内訳（機能別・モデル別・エージェント別）もそこから起こせる。
//
// 表示の約束（docs/46 §1-c の非交渉ライン）:
//   * **「0」と「未計測」を混同させない。** トークンを報告しない CLI は spend 0 になるが、
//     それは「使っていない」ではない。未計測の呼び出し回数を独立したタイルで出し、
//     coverage から自動生成した注記を常時添える（手書きの表はドリフトする）。
//   * **コストは副次指標。** claude だけ実測が返るので「API 換算相当額」と明記して
//     小さく出す。主指標は spend（= in + ccreate + out）。
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
import { fetchUsageSeries } from "./api.ts";
import type { UsageAgg, UsageDim, UsageSeries } from "./api.ts";
import { OTHER_KEY } from "./colors.ts";
import { breakdownRows, coverageNotes, matrixRows, perCall, rangeOf, stackModel } from "./series.ts";
import type { UsageMetric } from "./series.ts";

// 期間プリセット（dataviz: 日付レンジが読み手の最初に触るフィルタ）。
const RANGES: { hours: number; key: MsgKey }[] = [
  { hours: 24, key: "usage.range_24h" },
  { hours: 24 * 7, key: "usage.range_7d" },
  { hours: 24 * 30, key: "usage.range_30d" },
];

// 時系列の割り方。origin（出自）を並べているのが docs/46 §2-c の主眼 —「人が始めた
// セッション」と「オペレーター/定時が無人で立てたセッション」を同じ絵で対比する。
const BY_DIMS: { dim: UsageDim; key: MsgKey }[] = [
  { dim: "feature", key: "usage.by_feature" },
  { dim: "kind", key: "usage.by_kind" },
  { dim: "model", key: "usage.by_model" },
  { dim: "origin", key: "usage.by_origin" },
  { dim: "trigger", key: "usage.by_trigger" },
];

const METRICS: { metric: UsageMetric; key: MsgKey }[] = [
  { metric: "spend", key: "usage.metric_spend" },
  { metric: "calls", key: "usage.metric_calls" },
  { metric: "cread", key: "usage.metric_cread" },
  { metric: "cost_usd", key: "usage.metric_cost" },
];

/** 軸の値 → 表示名。未知の値（新しいモデル名など）はキーをそのまま出す。 */
export const dimLabel = (dim: string, key: string): string => {
  if (key === OTHER_KEY) return tMaybe("usage.other") ?? "other";
  if (key === "") return tMaybe("usage.empty_value") ?? "—";
  return tMaybe(`usage.val.${dim}.${key}`) ?? key;
};

// コストは claude の実測が有るときだけ入る。0 を "$0.0000" と書くと「タダで動いた」に
// 読めてしまうので、実測が無い＝「—」（未計測と同じ書き方）で出す。
const fmtUSD = (v: number): string => (v <= 0 ? "—" : v >= 1 ? "$" + v.toFixed(2) : "$" + v.toFixed(4));

/** 指標つきの数値整形。トークンは compact（fmtTok）、回数は桁区切り、コストは $。 */
export function fmtMetric(metric: UsageMetric, v: number): string {
  if (metric === "cost_usd") return fmtUSD(v);
  if (metric === "calls") return fmtNum(Math.round(v));
  return fmtTok(Math.round(v));
}

interface FilterTerm {
  dim: string;
  value: string;
}
const filterParam = (terms: FilterTerm[]): string => terms.map((f) => `${f.dim}:${f.value}`).join(",");

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

  const filter = filterParam(filters);

  const load = useCallback(
    async (signal: AbortSignal): Promise<boolean> => {
      if (!running) return true; // ワークスペース停止中は叩かない（起動後に deps で再実行）
      // now はリクエスト時刻で固定する（3本の系列が別々の "今" を見ないように）。
      const { from, to, bucket } = rangeOf(hours, new Date());
      const common = { from, to, filter: filter || undefined };
      const [a, b, c] = await Promise.all([
        fetchUsageSeries({ ...common, bucket, by }, signal),
        fetchUsageSeries({ ...common, by: "feature", split: "model" }, signal),
        fetchUsageSeries({ ...common, by: "kind", split: "model" }, signal),
      ]);
      if (signal.aborted) return true;
      // 過渡的な 502（ワークスペース起動直後に CP が返す）は再試行に回す。ここで
      // 空データを確定させると、エージェントが上がっても「記録なし」のまま固まる。
      if (isTransientErr(a) || isTransientErr(b) || isTransientErr(c)) return false;
      const bad = [a, b, c].find((r) => (r as { error?: unknown })?.error);
      if (bad) {
        setErr(errText((bad as { error: { code?: string; message?: string } }).error));
        setLoading(false);
        return true;
      }
      setErr("");
      setSeries(a as UsageSeries);
      setFeatModel(b as UsageSeries);
      setKindModel(c as UsageSeries);
      setLoading(false);
      return true;
    },
    [running, hours, by, filter],
  );
  useRetryLoad(load, [running, hours, by, filter, reloadTick]);

  // 内訳は matrix から起こす（追加リクエスト無し）: 機能別 = 行合計、モデル別 = 列合計、
  // エージェント別 = kind×model の行合計。
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
    if (value === OTHER_KEY) return; // 「その他」は実体ではないので絞り込めない
    setFilters((cur) =>
      cur.some((f) => f.dim === dim && f.value === value)
        ? cur.filter((f) => !(f.dim === dim && f.value === value))
        : [...cur, { dim, value }],
    );
  };
  const isFiltered = (dim: string, value: string) => filters.some((f) => f.dim === dim && f.value === value);

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

      {/* フィルタは1行に集約し、下の全チャート・表を同じスライスで再描画する
          （dataviz: チャートごとのフィルタは作らない）。 */}
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
        <button type="button" className="ghost uc-reload" onClick={() => setReloadTick((n) => n + 1)}>
          <Icon name="refresh" /> {tr("usage.reload")}
        </button>
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
            <Legend stack={stack} by={by} onPick={(k) => toggleFilter(by, k)} isOn={(k) => isFiltered(by, k)} />
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

          <CoverageBanner notes={notes} unmeasured={series?.unmeasured_calls || 0} />
        </div>
      )}
    </div>
  );
}

/** matrix から行合計 / 列合計を起こす（内訳バー用。追加リクエストを撃たないため）。 */
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

// 主指標 spend を大きく、cache_read とコストは併記（別軸のグラフは作らない — 2軸は
// 相関を捏造するので dataviz の禁じ手）。未計測は独立したタイルで、0 と混ざらない位置に。
function KpiRow({ totals, unmeasured }: { totals: UsageAgg | undefined; unmeasured: number }) {
  const tr = useT();
  const t = totals;
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
      <div className="ukpi" title={tr("usage.kpi_cost_hint")}>
        <div className="ukpi-val">{fmtUSD(t?.cost_usd || 0)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_cost")}</div>
      </div>
      <div className={"ukpi" + (unmeasured > 0 ? " unmeasured" : "")} title={tr("usage.kpi_unmeasured_hint")}>
        <div className="ukpi-val">{fmtNum(unmeasured)}</div>
        <div className="ukpi-lab muted">{tr("usage.kpi_unmeasured")}</div>
      </div>
    </div>
  );
}

// --- 積み上げ棒（時系列） -----------------------------------------------------

interface ChartProps {
  stack: ReturnType<typeof stackModel>;
  by: string;
  metric: UsageMetric;
  bucket: string;
}

const bucketLabel = (t: string, bucket: string): string =>
  bucket === "hour" ? fmtDateTime(t, TIME_HM) : fmtDateTime(t, { month: "numeric", day: "numeric" });

// 目盛りは 0 / 中間 / 上端の3本だけ（グリッドは1px実線・背景から1段だけ浮かせる）。
// 上端は「きりの良い数」に丸めるが、刻みを細かめに持つ（1,2,2.5,3,4,5,8,10）。粗い梯子だと
// 最大 3.3M が 5M に飛んで棒が縦半分しか使えず、日々の差が読みにくくなる。
// 半分の値も同時に目盛りになるので、割って汚くならない数だけを選んである。
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

  // 目盛りラベルの間引き。狭い幅（スマホ・ペイン）で 11本や 24本の日付を全部書くと
  // 文字が重なって読めなくなるので、幅を実測して n 本おきに出す。棒そのものは全部描く
  // （値はツールチップと表ビューで読める＝ラベルを間引いてもデータは隠れない）。
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
              // ツールチップは補助。同じ値は「表」ビューでも読める（色/ホバー任せにしない）。
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
              <span className="ux-tick muted">{i % stride === 0 ? bucketLabel(row.t, bucket) : " "}</span>
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

// 凡例は2系列以上で常に出す（色だけに identity を持たせない）。クリックでその系列に絞る。
function Legend({
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
  if (stack.legend.length < 2) return null;
  return (
    <div className="usage-legend">
      {stack.legend.map((l) => (
        <button
          key={l.key}
          type="button"
          className={"ulg" + (isOn(l.key) ? " on" : "")}
          onClick={() => onPick(l.key)}
          title={l.key === OTHER_KEY ? stack.foldedKeys.map((k) => dimLabel(by, k)).join(", ") : tr("usage.filter_add")}
        >
          <span className="ulg-sw" style={{ background: l.color }} />
          {dimLabel(by, l.key)}
        </button>
      ))}
    </div>
  );
}

// 表ビュー: グラフと同じ値を色抜きで読める WCAG 上の双子（ツールチップに値を閉じ込めない）。
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

// --- 内訳（横棒） -------------------------------------------------------------

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
              {/* 値は直接ラベル（明色スロットのコントラスト不足に対する relief でもある）。 */}
              <span className="ubd-val">{fmtMetric(metric, r.value)}</span>
            </button>
          ))}
        </div>
      )}
    </section>
  );
}

// --- 機能 × モデルの表 --------------------------------------------------------

// docs/46 §2-b の本命ビュー。「この機能はどのモデルで走っているか」— 補助呼び出しが
// CLI 既定のフラッグシップに流れていれば、ここに calls と平均で出る。
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
                <td className="num">{fmtNum(c.agg.calls)}</td>
                <td className="num">{fmtTok(c.agg.spend)}</td>
                <td className="num">{fmtTok(Math.round(perCall(c.agg)))}</td>
                <td className="num">{c.agg.cost_usd ? fmtUSD(c.agg.cost_usd) : "—"}</td>
              </tr>
            )),
          )}
        </tbody>
      </table>
    </div>
  );
}

// --- 未計測バナー -------------------------------------------------------------

// coverage はサーバが観測データから起こす。ここで文言を手書きしない（手書きの表は
// エージェントが増えた瞬間にドリフトする）。
function CoverageBanner({
  notes,
  unmeasured,
}: {
  notes: ReturnType<typeof coverageNotes>;
  unmeasured: number;
}) {
  const tr = useT();
  if (!notes.length && unmeasured === 0) return null;
  return (
    <section className="usage-card usage-coverage">
      <div className="uc-head">
        <h4>
          <Icon name="info" /> {tr("usage.coverage_title")}
        </h4>
      </div>
      {unmeasured > 0 && <p className="uc-sub">{tr("usage.coverage_unmeasured", { n: fmtNum(unmeasured) })}</p>}
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
