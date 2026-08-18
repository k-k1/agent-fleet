// features/usage/api — GET /api/usage/series の型と取得（docs/46 §4-a / ADR0029）。
//
// サーバが集計して返す（Console に生ログは流れない）。ワイヤ形は Go 側
// workspace/agent/usage_series.go の usageSeriesResp と1対1。軸の語彙は
// **サーバが未知の軸を 400 で弾く**ので、こちら側も UsageDim に閉じておく。
import { api } from "../../core/api/client.ts";

/** 集計軸。サーバの validUsageDim と同じ語彙（増減は両側同時に）。 */
export type UsageDim =
  | "feature"
  | "kind"
  | "model"
  | "trigger"
  | "origin"
  | "origin_conv"
  | "verb"
  | "model_src"
  | "measured";

/** 集計値（series の1要素）。spend = in + ccreate + out（cache_read は含めない）。 */
export interface UsageAgg {
  spend: number;
  in: number;
  out: number;
  cread: number;
  ccreate: number;
  calls: number;
  cost_usd?: number;
}

export interface UsageBucket {
  t: string; // バケット先頭時刻（RFC3339・UTC）
  series: Record<string, UsageAgg>;
}

/** この kind が何をどこまで報告するかの自己申告（データから自動生成される）。 */
export interface UsageCoverage {
  tokens: "exact" | "partial" | "none" | string;
  model: "reported" | "requested" | "none" | string;
}

export interface UsageSeries {
  from: string;
  to: string;
  bucket: "day" | "hour" | string;
  by: string;
  split?: string;
  buckets: UsageBucket[];
  totals: UsageAgg;
  /** split 指定時のみ（「機能 × モデル」等）。matrix[by][split] = 集計値。 */
  matrix?: Record<string, Record<string, UsageAgg>>;
  coverage: Record<string, UsageCoverage>;
  unmeasured_calls: number;
  /** 要求期間の一部が raw の保持期間より古く hour 粒度で復元できなかった。 */
  truncated?: boolean;
  /**
   * セッション本体の台帳への折り込みが、この読み出しの時点で走っていた。
   * **＝この応答は直近のターンをまだ含まないかもしれない。** 折り込みは非同期なので、
   * これが立っている間は落ち着くまで取り直す（UsageView）。
   */
  folding?: boolean;
}

export interface UsageQuery {
  from?: string;
  to?: string;
  bucket?: "day" | "hour";
  by?: UsageDim;
  split?: UsageDim;
  /** 同一軸 OR・異軸 AND。末尾 * のみ前方一致（例: kind:claude,feature:title.*）。 */
  filter?: string;
  include?: string;
  /**
   * "force" でセッション折り込みの 60 秒スロットルを飛ばす。**明示的な再取得だけ**に付ける
   * （自動の取り直しに付けると、折り込みが終わるたびに次を起動して永久に走り続ける）。
   */
  fold?: "force";
}

export function usageSeriesPath(q: UsageQuery): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v));
  const qs = p.toString();
  return "api/usage/series" + (qs ? "?" + qs : "");
}

/** 系列を1本取得。api() は失敗を throw せず {error} で解決するので、判定は呼び出し側で。 */
export function fetchUsageSeries(q: UsageQuery, signal?: AbortSignal): Promise<UsageSeries | { error: unknown }> {
  return api(usageSeriesPath(q), signal ? { signal } : undefined);
}

// --- rtk 効果（トークン節約） -------------------------------------------------
// 上の台帳（CP 集計）とは別系。コンテナ内 rtk 自身の履歴 DB を Agent が
// `rtk gain --all --format json` で読み、透過で返す（スキーマは rtk のもの —
// workspace/agent/agent_rtk.go handleAgentRTKGain）。rtk 不在や失敗は
// available/error 入りの soft ボディで 200 が返る（呼び出し側は黙って隠す）。
// 古い Agent（--daily 時代）は daily だけ返す＝weekly/monthly は無い前提で読むこと。

/** 1バケット分。粒度で日付キーが変わる（daily=date / weekly=week_start,week_end / monthly=month）。 */
export interface RtkGainBucket {
  date?: string; // "YYYY-MM-DD"（タイムゾーン無しの素の日付）
  week_start?: string;
  week_end?: string;
  month?: string; // "YYYY-MM"
  commands?: number;
  input_tokens?: number;
  output_tokens?: number;
  saved_tokens?: number;
  savings_pct?: number;
  total_time_ms?: number;
  avg_time_ms?: number;
}

export interface RtkGainSummary {
  total_saved?: number;
  avg_savings_pct?: number;
  total_input?: number;
  total_output?: number;
  total_commands?: number;
  total_time_ms?: number;
  avg_time_ms?: number;
}

export interface RtkGain {
  available?: boolean;
  error?: string;
  summary?: RtkGainSummary;
  daily?: RtkGainBucket[];
  weekly?: RtkGainBucket[];
  monthly?: RtkGainBucket[];
}

export function fetchRtkGain(signal?: AbortSignal): Promise<RtkGain | { error: unknown }> {
  return api("api/agents/rtk/gain", signal ? { signal } : undefined);
}
