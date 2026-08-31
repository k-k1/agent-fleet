// UsageView — 機能別トークン使用量のダッシュボード（docs/log/46 P4 / ADR0029 §7-5）。
//
// **モーダル非依存の純粋コンポーネント**として書く。今は設定モーダルの「使用量」タブが
// 薄いラッパとして差しているだけで、将来ペインに昇格させたくなったら PaneKind を足して
// 同じ View を差せる（逆はできない＝ペイン前提で書くと設定に入らないので、この順序）。
//
// 読むもの: GET /api/usage/series（サーバ側で集計済み。生ログは流れてこない）。
// 1画面で3リクエスト — 選択中の軸の時系列 / 機能×モデル / エージェント×モデル。後ろ2つは
// matrix なので、内訳（機能別・モデル別・エージェント別）もそこから起こせる。加えて末尾の
// rtk 効果カードだけ別系（GET /api/agents/rtk/gain — RtkGainCard 参照）を1回読む。
//
// 表示の約束（docs/log/46 §1-c の非交渉ライン）:
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

// 期間プリセット（dataviz: 日付レンジが読み手の最初に触るフィルタ）。
const RANGES: { hours: number; key: MsgKey }[] = [
  { hours: 24, key: "usage.range_24h" },
  { hours: 24 * 7, key: "usage.range_7d" },
  { hours: 24 * 30, key: "usage.range_30d" },
];

// 時系列の割り方。origin（出自）を並べているのが docs/log/46 §2-c の主眼 —「人が始めた
// セッション」と「オペレーター/定時が無人で立てたセッション」を同じ絵で対比する。
const BY_DIMS: { dim: UsageDim; key: MsgKey }[] = [
  { dim: "feature", key: "usage.by_feature" },
  { dim: "kind", key: "usage.by_kind" },
  { dim: "model", key: "usage.by_model" },
  { dim: "origin", key: "usage.by_origin" },
  { dim: "trigger", key: "usage.by_trigger" },
];

// 折り込み待ちの再取得。実測で十数秒かかる（158 セッションで ~20s）ので、2 秒 × 30 回 =
// 最大 1 分待つ。上限に当たっても壊れた表示にはならない（申告バッジが残るだけ）。
const FOLD_POLL_MS = 2000;
const FOLD_POLL_MAX = 30;

const METRICS: { metric: UsageMetric; key: MsgKey }[] = [
  { metric: "spend", key: "usage.metric_spend" },
  { metric: "calls", key: "usage.metric_calls" },
  { metric: "cread", key: "usage.metric_cread" },
  // 金額は**推定**を出す（実測はセッション本体に無く、あの列がずっと「—」だった）。
  // 実測は消さず、推定の隣にツールチップで併記する（別の計測法なので足さない）。
  { metric: "cost_est_usd", key: "usage.metric_cost" },
];

/** 軸の値 → 表示名。未知の値（新しいモデル名など）はキーをそのまま出す。 */
export const dimLabel = (dim: string, key: string): string => {
  if (key === OTHER_KEY) return tMaybe("usage.other") ?? "other";
  if (key === "") return tMaybe("usage.empty_value") ?? "—";
  return tMaybe(`usage.val.${dim}.${key}`) ?? key;
};

// 0 を "$0.0000" と書くと「タダで動いた」に読めてしまうので、値が無い＝「—」（未計測と
// 同じ書き方）で出す。
const fmtUSD = (v: number): string => (v <= 0 ? "—" : v >= 1 ? "$" + v.toFixed(2) : "$" + v.toFixed(4));

// 推定額は必ず「≈」を付ける。実測（claude の補助呼び出し）と同じ書体で並ぶと、単価表 ×
// トークンで起こした数字が請求額として読まれる。
export const fmtUSDEst = (v: number): string => (v <= 0 ? "—" : "≈" + fmtUSD(v));

// 単価（$/100万トークン）。末尾の 0 を残すと $2.00 / $0.0200 と並んで読みにくいので落とす。
const fmtRate = (v: number): string => "$" + String(v >= 1 ? +v.toFixed(2) : +v.toFixed(4));

/** 単価の出所を1行で。catalog:<provider>/<model> は provider まで出す（検算の手掛かり）。 */
export function priceSrcLine(price: UsagePrice | undefined): string {
  if (!price) return "";
  const [kind, ref] = price.src.split(/:(.+)/);
  return kind === "catalog"
    ? (tMaybe("usage.price_src_catalog") ?? "").replace("{ref}", ref || "")
    : (tMaybe("usage.price_src_builtin") ?? "");
}

/** 金額セルのツールチップ。単価・出所・実測を積む（金額だけでは検算できない）。 */
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

/** 指標つきの数値整形。トークンは compact（fmtTok）、回数は桁区切り、コストは $。 */
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
  // セッション本体の消費は「読まれた時に転写から台帳へ折り込む」— しかも**非同期**なので、
  // 折り込みが走っている間の応答は直近ターンを含まない（docs/log/46 §3-b）。サーバはそれを
  // folding で申告してくるので、落ち着くまでこちらで取り直す。これが無いと、利用者が
  // 「最新にならない」まま再取得を何度も押して当てにいく画面になる（実際にそうなっていた）。
  const [folding, setFolding] = useState(false);
  const [foldTick, setFoldTick] = useState(0);
  const foldTries = useRef(0);
  // 明示的な再取得だけスロットルを飛ばす（自動の取り直しに付けると走り続ける）。
  const forceFold = useRef(false);

  const filter = filterParam(filters);

  const load = useCallback(
    async (signal: AbortSignal): Promise<boolean> => {
      if (!running) return true; // ワークスペース停止中は叩かない（起動後に deps で再実行）
      // now はリクエスト時刻で固定する（3本の系列が別々の "今" を見ないように）。
      const { from, to, bucket } = rangeOf(hours, new Date());
      const fold = forceFold.current ? ("force" as const) : undefined;
      const common = { from, to, filter: filter || undefined, fold };
      const [a, b, c] = await Promise.all([
        fetchUsageSeries({ ...common, bucket, by }, signal),
        fetchUsageSeries({ ...common, by: "feature", split: "model" }, signal),
        fetchUsageSeries({ ...common, by: "kind", split: "model" }, signal),
      ]);
      if (signal.aborted) return true;
      // 過渡的な 502（ワークスペース起動直後に CP が返す）は再試行に回す。ここで
      // 空データを確定させると、エージェントが上がっても「記録なし」のまま固まる。
      if (isTransientErr(a) || isTransientErr(b) || isTransientErr(c)) return false;
      // ここから先は終端（成功でも本物のエラーでも）— force は消費済みにする。再試行に
      // 回した分では消さない（502 で消すと、押した再取得がスロットルに当たって空振りする）。
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

  // 折り込みが終わるまでの自動再取得。上限を置くのは、サーバが何かの理由で folding を
  // 落とし損ねた時に永久ポーリングへ落ちないため（止まった先は「再取得」が救う）。
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
    setFilters((cur) =>
      cur.some((f) => f.dim === dim && f.value === value)
        ? cur.filter((f) => !(f.dim === dim && f.value === value))
        : [...cur, { dim, value }],
    );
  };
  const isFiltered = (dim: string, value: string) => filters.some((f) => f.dim === dim && f.value === value);

  // 「その他」は実体ではないので、畳まれた実キー全部の OR へ展開して絞る（series.ts）。
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
        <button type="button" className="ghost uc-reload" onClick={reload}>
          <Icon name="refresh" /> {tr("usage.reload")}
        </button>
        {/* 折り込み中である事実を出す。黙って古い数字を見せると「反映されない」になり、
            利用者は再取得を連打する（それが直前までの挙動だった）。 */}
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

// 粒度ごとの表示バケット数（rtk は全履歴を返すので末尾だけ描く）。
const RTK_MODES = [
  { mode: "daily", n: 30, key: "usage.rtk_daily" },
  { mode: "weekly", n: 26, key: "usage.rtk_weekly" },
  { mode: "monthly", n: 24, key: "usage.rtk_monthly" },
] as const;
type RtkMode = (typeof RTK_MODES)[number]["mode"];

// rtk の日付は素の "YYYY-MM-DD" / "YYYY-MM"（タイムゾーン無し）。Date に文字列のまま
// 渡すと UTC 深夜扱いになり、負オフセットのロケールで前日／前月にずれるので、ローカル
// 日付として手で組む。
const rtkDate = (s: string): Date => {
  const [y, m, d] = s.split("-").map(Number);
  return new Date(y || 1970, (m || 1) - 1, d || 1);
};

/** バケットの軸ラベル。long はツールチップ／表の行見出し用（月次は年も出す）。 */
const rtkBucketLabel = (b: RtkGainBucket, mode: RtkMode, long = false): string => {
  if (mode === "monthly")
    return fmtDateTime(rtkDate(b.month || ""), long ? { year: "numeric", month: "short" } : { month: "short" });
  const t = rtkDate((mode === "weekly" ? b.week_start : b.date) || "");
  return fmtDateTime(t, { month: "numeric", day: "numeric" });
};

/** 実行時間の短い表示（ms→s→m→h）。トークンではないので fmtTok は使わない。 */
const fmtDur = (ms: number): string => {
  if (!isFinite(ms) || ms <= 0) return "—";
  if (ms < 1000) return Math.round(ms) + "ms";
  if (ms < 60_000) return (ms / 1000).toFixed(1) + "s";
  if (ms < 3_600_000) return Math.round(ms / 60_000) + "m";
  return (ms / 3_600_000).toFixed(1) + "h";
};

// RtkGainCard — rtk 効果（トークン節約）。台帳とは別系のコンテナ内計測（api.ts の
// fetchRtkGain 参照）で、ヘッドライン数値は全期間の累積。上の期間プリセット／フィルタ
// には連動せず、代わりに rtk が持つ粒度（日次/週次/月次）を自前のセグメントで切り替える
// （再読込ボタンだけ共有）。設定 > エージェントの RTK トグルの「結果」側 — かつて設定
// タブに居たが、監視は設定ではないのでダッシュボードのここに一枚で置く。
// rtk 不在・エラー・節約ゼロは丸ごと自己非表示（WsBar チップ時代からの約束）。
// 節約は正の値・単一系列なので、リソースの warn/crit ではなくアクセント1色で塗り、
// 凡例は置かない（タイトルが系列名）。値はツールチップと表ビューの両方で読める。
function RtkGainCard({ reloadTick }: { reloadTick: number }) {
  const tr = useT();
  const [gain, setGain] = useState<RtkGain | null>(null);
  const [mode, setMode] = useState<RtkMode>("daily");
  const [tableView, setTableView] = useState(false);
  const load = useCallback(async (signal: AbortSignal): Promise<boolean> => {
    const r = await fetchRtkGain(signal);
    if (signal.aborted) return true;
    if (isTransientErr(r)) return false; // WS 起動直後の 502 は retryLoad に回す
    setGain(r as RtkGain);
    return true;
  }, []);
  useRetryLoad(load, [reloadTick]);

  const s = gain?.summary;
  const saved = s?.total_saved || 0;
  if (!s || saved <= 0) return null;
  const pct = Math.round(s.avg_savings_pct || 0);
  // 古い Agent は daily しか返さない — 実際にデータのある粒度だけセグメントに出す。
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

// StackChart と同じ .ux-* の絵柄・ホバー・ラベル間引きを、単一系列（節約トークン）用に
// 薄く焼き直したもの。共有部品化しないのは、あちらのツールチップが「系列の内訳」で
// こちらは「同一バケットの別指標（節約率・コマンド数）」だから — 形が違う。
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
              {/* 間引いた目盛りは NBSP(" ")。普通の空白だと潰れて tick が高さ 0 になり、
                  .ux-cols の align-items:flex-end で列ごと基線より下に沈む（実バグ）。 */}
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
  // 実測は消さずに併記する（推定の答え合わせになる唯一の値）。足し算はしない。
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

// 凡例は2系列以上、**または畳み込みがある時**に出す（色だけに identity を持たせない）。
// 畳みがあるのに凡例を隠すと「名前のないグレーの棒」だけが残り、何の消費か読めなくなる
// （色スロットを持たない feature が1つだけ出た時にまさにこれが起きていた）。
// クリックでその系列に絞る — 「その他」は畳まれた実キー全部の OR で絞る。
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

// docs/log/46 §2-b の本命ビュー。「この機能はどのモデルで走っているか」— 補助呼び出しが
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
                {/* 1呼び出しが複数モデルに割れた時、回数は最も消費したモデル1行にだけ付く
                    （サーバ側 aggregateUsageRows）。0 回・消費ありの行はその相方なので、
                    「1回あたり」を 0 と書かずに — を出し、理由をツールチップに置く。 */}
                <td className="num" title={c.agg.calls === 0 && c.agg.spend > 0 ? tr("usage.calls_shared") : undefined}>
                  {fmtNum(c.agg.calls)}
                </td>
                <td className="num">{fmtTok(c.agg.spend)}</td>
                <td className="num">{c.agg.calls > 0 ? fmtTok(Math.round(perCall(c.agg))) : "—"}</td>
                {/* 推定額。単価表に無いモデルは 0 ではなく「—」＋理由をツールチップに置く
                    （0 と「値付けできない」を混同させない）。使った単価と出所、実測が
                    あればそれも同じツールチップに積む。 */}
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

// --- 未計測バナー -------------------------------------------------------------

// coverage はサーバが観測データから起こす。ここで文言を手書きしない（手書きの表は
// エージェントが増えた瞬間にドリフトする）。
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
  // 推定額をいくらぶんの消費から起こせたか。値付けできない分を黙っていると、
  // 「≈$41」が全消費ぶんの金額として読まれる。
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
      {/* 四捨五入で 0% になる端数を「消費の 0% は…」と書くと自己矛盾した注記になるので、
          1% 未満は専用の言い方にする（実データで踏んだ: 57k / 34.1M）。 */}
      {unpriced > 0 && (
        <p className="uc-sub">
          {unpricedPct >= 1
            ? tr("usage.coverage_unpriced", { pct: String(unpricedPct), n: fmtTok(unpriced) })
            : tr("usage.coverage_unpriced_sub1", { n: fmtTok(unpriced) })}
        </p>
      )}
      {/* カタログの取得日を必ず出す。推定額は保存していないので、カタログが更新されると
          過去の金額も変わる —— どの時点の単価かを言わずに額だけ動かすのは黙って嘘に近い。 */}
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
