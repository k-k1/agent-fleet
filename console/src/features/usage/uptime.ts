// 稼働時間ヒートマップの純関数層（docs/log/83）。縦 24 時間 × 横 日付。
//
// ⚠️ ここには DOM も api クライアントも入れない。`core/api/client.ts` はモジュール初期化で
// localStorage を触るので、値を 1 つでも取り込むと node 環境の vitest が落ちる
// （使用量グラフの series.ts で踏んだのと同じ）。型だけ import する。
//
// ⚠️ **これは金額ではない。** 実費は日単位でしか取れず、時間別の金額は見積にしかならない
// （ADR 0048 決定 2）。マスが表すのは占有と本数だけで、色は値段を意味しない。

/** CP が返す 1 メンバーの 1 時間（GET /api/usage/me/hourly ほか）。ゼロは省略されて届く。 */
export type UptimePoint = {
  hour: string; // YYYY-MM-DDTHH（UTC）
  samples?: number;
  running_secs?: number;
  measured_secs?: number;
  session_secs?: number;
  busy_secs?: number;
  max_sessions?: number;
  max_busy?: number;
};

export type UptimeMember = {
  tenant: string;
  user_key: string;
  email: string;
  hours: UptimePoint[];
};

export type UptimeResponse = {
  from: string;
  to: string;
  interval_secs: number;
  /** サンプラが動いていた時間（UTC）と、その時間に見た回数。これが無い時間は
   * 「止まっていた」ではなく「分からない」。⚠️ samples は**マスの分母**でもある。 */
  observed: UptimePoint[];
  members: UptimeMember[];
};

/** マスに出す指標。1 マスに 1 指標だけ — 色と別の何かで二重に符号化しない。 */
export type UptimeMetric = "running" | "sessions" | "busy";

/** 1 メンバーの寄与（ホバーの内訳用）。 */
export type UptimeContribution = {
  label: string;
  runningSecs: number;
  sessionSecs: number;
  busySecs: number;
};

/** 1 マス。ローカル時刻の (日, 時) に畳んだもの。 */
export type UptimeCell = {
  day: string; // ローカルの YYYY-MM-DD
  hour: number; // ローカルの 0..23
  /** サンプラがこの時間を見ていたか。false = 空白（灰色＝止まっていた、ではない）。 */
  observed: boolean;
  /** マスの分母＝**サンプラがこの時間を見ていた秒数**（ハートビートの samples × 間隔）。
   *
   * ⚠️ 1 時間を 3600 秒と決め打たない。まだ途中の「今の時間」と、CP が落ちていた時間が
   * どちらも薄くなり、色が稼働ではなく**観測の欠け**を表してしまう。
   * ⚠️ メンバーの samples を足し合わせたものでもない。それだと合算のマスが
   * 「平均何台動いていたか」ではなく「動いていたワークスペースの割合」になる。 */
  possibleSecs: number;
  /** ハートビートを取り損ねた時間だけで使う予備の分母（メンバー行の samples の最大）。 */
  fallbackSecs: number;
  runningSecs: number;
  measuredSecs: number;
  sessionSecs: number;
  busySecs: number;
  maxSessions: number;
  maxBusy: number;
  contributions: UptimeContribution[];
};

/** UTC の時バケットキーを、閲覧者のローカル時刻の (日, 時) に落とす。
 *
 * ⚠️ サーバは UTC でしか刻まない。ここでずらさないと、日本の利用者のヒートマップは
 * 9 時間ずれて「毎晩深夜に働いている」という表になる。
 * ⚠️ +05:30 のような 30 分オフセットでは 1 つの UTC 時間がローカルの時間境界をまたぐ。
 * その場合は開始時刻の属する時間に丸める（画面に注記を出す）。
 */
export function localBucket(utcHour: string): { day: string; hour: number } | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2})$/.exec(utcHour);
  if (!m) return null;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4]));
  if (Number.isNaN(d.getTime())) return null;
  return { day: localDayKey(d), hour: d.getHours() };
}

/** ローカルの YYYY-MM-DD。toISOString は UTC に戻してしまうので使わない。 */
export function localDayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** [from, to] のローカル日付を並べる（両端含む）。 */
export function dayRange(from: string, to: string): string[] {
  const out: string[] = [];
  const start = new Date(from + "T00:00:00");
  const end = new Date(to + "T00:00:00");
  if (Number.isNaN(start.getTime()) || Number.isNaN(end.getTime())) return out;
  for (let d = start; d <= end && out.length < 400; d = new Date(d.getTime() + 86400000)) {
    out.push(localDayKey(d));
  }
  return out;
}

/** from の 1 日前 / to の 1 日後。サーバは UTC 日で切るので、ローカルの端のマスを
 * 埋めるには前後 1 日ぶん余分に貰う必要がある（+09:00 なら UTC の 15:00 が翌日の 0 時）。 */
export function widenForTimezone(from: string, to: string): { from: string; to: string } {
  const shift = (day: string, days: number) =>
    localDayKey(new Date(new Date(day + "T00:00:00").getTime() + days * 86400000));
  return { from: shift(from, -1), to: shift(to, 1) };
}

/** メンバーの表示名。user_key を優先し、無ければメール、どちらも無ければ「不明」に相当する空。 */
export function memberLabel(m: UptimeMember): string {
  return m.user_key || m.email || "";
}

const EMPTY_CELL = (day: string, hour: number): UptimeCell => ({
  day,
  hour,
  observed: false,
  possibleSecs: 0,
  fallbackSecs: 0,
  runningSecs: 0,
  measuredSecs: 0,
  sessionSecs: 0,
  busySecs: 0,
  maxSessions: 0,
  maxBusy: 0,
  contributions: [],
});

/** 応答をローカル時刻のマス目に畳む。キーは `day|hour`。
 *
 * ⚠️ observed は**ハートビートだけ**から作る。メンバーの行の有無から推測すると、
 * CP が落ちていた時間と誰も働かなかった時間が同じ灰色になる。
 */
export function buildGrid(res: UptimeResponse | null): Map<string, UptimeCell> {
  const grid = new Map<string, UptimeCell>();
  if (!res) return grid;
  const interval = res.interval_secs > 0 ? res.interval_secs : 0;
  const at = (day: string, hour: number) => {
    const k = day + "|" + hour;
    let c = grid.get(k);
    if (!c) {
      c = EMPTY_CELL(day, hour);
      grid.set(k, c);
    }
    return c;
  };
  for (const h of res.observed || []) {
    const b = localBucket(h.hour);
    if (!b) continue;
    const c = at(b.day, b.hour);
    c.observed = true;
    c.possibleSecs += (h.samples || 0) * interval;
  }
  for (const m of res.members || []) {
    const label = memberLabel(m);
    for (const p of m.hours || []) {
      const b = localBucket(p.hour);
      if (!b) continue;
      const c = at(b.day, b.hour);
      // ⚠️ ハートビートが無いのに稼働している時間の保険。分母 0 で割ると Infinity が
      // そのまま段の計算に入る。この行が見られた回数を下限として使う。
      c.fallbackSecs = Math.max(c.fallbackSecs, (p.samples || 0) * interval);
      c.runningSecs += p.running_secs || 0;
      c.measuredSecs += p.measured_secs || 0;
      c.sessionSecs += p.session_secs || 0;
      c.busySecs += p.busy_secs || 0;
      c.maxSessions = Math.max(c.maxSessions, p.max_sessions || 0);
      c.maxBusy = Math.max(c.maxBusy, p.max_busy || 0);
      if (p.running_secs) {
        c.contributions.push({
          label,
          runningSecs: p.running_secs || 0,
          sessionSecs: p.session_secs || 0,
          busySecs: p.busy_secs || 0,
        });
      }
    }
  }
  for (const c of grid.values()) {
    // 寄与の多い順。ホバーで上から 3 件だけ出すので、並び順そのものが情報になる。
    c.contributions.sort((a, b) => b.sessionSecs - a.sessionSecs || b.runningSecs - a.runningSecs);
  }
  return grid;
}

/** マスの値。指標ごとに 1 つだけ。
 *
 * - running: 見ていた時間のうち動いていた割合。**1 人なら 0..1 の稼働率、合算なら
 *   「平均で何台動いていたか」**——分母が「その時間」1 つぶんなので、同じ式が両方になる。
 * - sessions / busy: 動いていた「あいだ」の平均同時本数。⚠️ 分母は running ではなく
 *   measured — Agent に届かなかった時間まで分母に入れると、忙しかった時間が薄まる。
 */
export function cellValue(c: UptimeCell | undefined, metric: UptimeMetric): number {
  if (!c) return 0;
  if (metric === "running") {
    const denom = c.possibleSecs || c.fallbackSecs;
    return denom > 0 ? c.runningSecs / denom : 0;
  }
  if (c.measuredSecs <= 0) return 0;
  return (metric === "busy" ? c.busySecs : c.sessionSecs) / c.measuredSecs;
}

/** 本数が分からない時間か（起きてはいたが Agent に届かなかった）。
 * ⚠️ 0 本と同じ見た目にしない。 */
export function isUnmeasured(c: UptimeCell | undefined, metric: UptimeMetric): boolean {
  return metric !== "running" && !!c && c.runningSecs > 0 && c.measuredSecs <= 0;
}

/** 色の段（1..5）。
 *
 * ⚠️ 段は 5 つしか無いので、指標によって 0 の扱いを変える。本数の指標では
 * 「起きていたが 0 本」が最下段（薄いオレンジ）で、灰色（止まっていた）とは別物である。
 * 稼働率の指標では 0 のマスは描かれない（＝止まっていた）ので、5 段を全部使える。
 */
export function cellLevel(value: number, max: number, zeroFloor: boolean): number {
  if (max <= 0) return 1;
  const v = Math.max(0, Math.min(1, value / max));
  if (zeroFloor) return v <= 0 ? 1 : Math.min(5, 1 + Math.ceil(v * 4));
  return Math.min(5, Math.max(1, Math.ceil(v * 5)));
}

/** 凡例と段分けの基準になる最大値。⚠️ 最大 0 のときに 0 を返すと段が全部 1 になるので
 * 呼び出し側が段の計算で守る（ここでは素直に最大を返す）。 */
export function maxValue(grid: Map<string, UptimeCell>, days: string[], metric: UptimeMetric): number {
  let max = 0;
  for (const day of days) {
    for (let h = 0; h < 24; h++) {
      max = Math.max(max, cellValue(grid.get(day + "|" + h), metric));
    }
  }
  return max;
}

/** 凡例の帯の下限値。段 n が表す値域の下端（表示用）。 */
export function levelBand(level: number, max: number, zeroFloor: boolean): [number, number] {
  if (zeroFloor) {
    if (level <= 1) return [0, 0];
    const step = max / 4;
    return [(level - 2) * step, (level - 1) * step];
  }
  const step = max / 5;
  return [(level - 1) * step, level * step];
}

/** 秒 → 「1時間2分」相当の短い表記に使う分数。端数は切り上げ（0 分と表示して
 * 「動いていない」と読ませない）。 */
export function minutesOf(secs: number): number {
  return secs > 0 ? Math.max(1, Math.round(secs / 60)) : 0;
}

/** ローカルのタイムゾーンが時間の境界に揃っているか。揃っていなければマスは
 * 30 分ずれて丸められるので、画面に注記を出す必要がある。 */
export function timezoneAlignsToHours(d: Date = new Date()): boolean {
  return d.getTimezoneOffset() % 60 === 0;
}
