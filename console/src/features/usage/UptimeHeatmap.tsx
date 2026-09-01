// 稼働時間ヒートマップ — 縦 24 時間 × 横 日付（docs/log/83）。
//
// なぜこれが要るか: 「稼働時間」も「クラウド費用」も 1 日 1 つの数字しか持たない。だから
// 「金曜だけ高い」までは分かっても、**止め忘れなのか働いていたのか**が分からない。マスを
// 時間まで割ると、その 2 つは形で見分けがつく（夜通し薄いオレンジが続く＝止め忘れ、
// 昼だけ濃い＝働いていた）。
//
// ⚠️ **金額は塗らない。** 実費（Cost Explorer）は日単位でしか取れないので、時間別の金額は
// 「秒 × 誰かが一度打ち込んだ単価」＝見積にしかならず、ADR 0048 決定 2 がそれを否定して
// いる。ここが「クラウド費用」ではなく「稼働時間」の面である理由でもある。
//
// ⚠️ マスは **3 値**（未観測 / 停止 / 稼働）。灰色は「止まっていた」で、空白は
// 「CP が見ていなかった」。ここを 2 値に潰すと、CP が落ちていた日が「誰も働かなかった日」
// として自信たっぷりに表示される。
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

// 既定の期間はローカルの直近 14 日。30 日 × 24 段は横に詰まりすぎて、日付ラベルが
// 「08-1708-18」と連結する（クラウド費用の棒グラフで実際に踏んだ）。
function defaultRange(): { from: string; to: string } {
  const now = new Date();
  return {
    from: localDayKey(new Date(now.getTime() - (DEFAULT_DAYS - 1) * 86400000)),
    to: localDayKey(now),
  };
}

/** 値の表示。凡例・読み出し・表の 3 か所で同じ関数を通す（同じ数字が場所によって違う
 * 形で出るのを防ぐ）。
 *
 * ⚠️ 1 人ぶんの稼働率は 100% で頭打ちにする。スイープが途中で失敗した時間はメンバーの
 * samples がハートビートを上回りうるので、素直に割ると「103% 稼働」が出る。 */
function fmtValue(v: number, metric: UptimeMetric, aggregate: boolean): string {
  if (metric !== "running") return v.toFixed(1);
  if (aggregate) return v.toFixed(1);
  return Math.round(Math.min(1, v) * 100) + "%";
}

// 日付ラベルの間引き。
//
// ⚠️ 下限が 2 なのは列幅の問題で、本数の問題ではない。「08-19」は約 45px あるのに列は
// 22px しかないので、3 日ぶんを選ぶと stride 1 になって隣とくっつく（クラウド費用の
// 棒グラフが「08-1708-18」になったのと同じ壊れ方）。
function labelStride(n: number): number {
  return Math.max(2, Math.ceil(n / 8));
}

/** 期間つきのデータ取得。費用の面と同じく「適用」でしか取り直さない（日付入力は
 * 1 文字ごとに変わるので、依存に入れると打っている最中に取得が走る）。 */
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
        // ⚠️ サーバは UTC 日で切る。ローカルの端のマスを埋めるには前後 1 日ぶん余分に
        // 貰う必要がある（+09:00 なら UTC の 15:00 がローカルの翌日 0 時のマス）。
        const w = widenForTimezone(win.from, win.to);
        // ⚠️ クエリの組み立てはここ 1 か所。呼び出し側が endpoint に `?tenant=` を
        // 足す形にすると `?tenant=x?from=y` という壊れた URL が黙って通る。
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

  // ⚠️ 期間は「適用」でしか取り直さない（日付入力は 1 文字ごとに変わる）。テナントの
  // 切り替えは選んだ瞬間に取り直す — あちらは確定した選択である。
  useEffect(() => {
    load(defaultRange());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [endpoint, tenant]);

  return { range, setRange, applied, data, err, loading, apply: () => load(range) };
}

/** ホバー / フォーカスの読み出し。値が先、ラベルが後（読み手は場所を知っていて数字が
 * 欲しい）。⚠️ tooltip は補助であって唯一の経路にしない — 同じ内容が表ビューにもある。 */
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

/** 凡例。⚠️ 2 つ以上の意味を色で分けている以上、凡例は常に出す（色だけで意味を
 * 運ばないための最低限）。 */
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

/** 表ビュー。⚠️ 色だけで値を運ばないための逃げ道であって、おまけではない
 * （凡例・読み出し・この表の 3 つで、色を見分けられない読み手にも全部届く）。 */
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

/** ヒートマップ本体。データ取得は呼び出し側（3 つの入口で同じ形の応答が来る）。
 *
 * aggregate=true は管理の合算。マスの値は「その時間に平均で何台動いていたか」になり、
 * 内訳はホバーに出る。合算そのものではなくメンバー別の系列を受け取って**ここで積む**ので、
 * 合計と内訳が別の API から来て食い違う、ということが起きない。 */
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
            // ⚠️ 1fr にしない。14 列を横いっぱいに伸ばすと 1 マスが横棒になって
            // ヒートマップに見えなくなる（headless で実測）。上限つきで左寄せする。
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

      {/* ⚠️ 但し書きは脚注にしない。「サンプル間隔ぶんの粗さ」と「30 分ずれる時間帯」は
          どちらもマスの読み方そのものを変える。 */}
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
        // ⚠️ 状態は色だけで運ばない。aria-label に日時と状態を書いておくと、
        // 読み上げでも「9/1 10 時 停止」と読める。
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

/** 期間の入力。費用の面と同じ並び（日付 → 適用）。 */
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

/** MyUptimeView — 本人の設定の「稼働時間」タブ。
 *
 * ⚠️ 本人向けの面に他人の稼働は 1 件も来ない（CP が本人のぶんだけ返す）。 */
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

/** MemberUptimePanel — 管理のメンバー詳細に差す 1 枚。
 *
 * 隣に「ワークスペースを強制停止」と「ディスク上限」が並んでいる面なので、
 * 「夜通し薄いオレンジ」＝止め忘れ、はそのままその操作の根拠になる。 */
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

/** UptimeAdminView — 管理の「稼働時間」に出す合算 1 枚。tenant はテナント選択の slug
 * （空 = デプロイ全体・super_admin のみ）。 */
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
