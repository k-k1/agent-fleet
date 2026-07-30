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
// `rtk gain --daily --format json` で読み、透過で返す（スキーマは rtk のもの —
// workspace/agent/agent_rtk.go handleAgentRTKGain）。rtk 不在や失敗は
// available/error 入りの soft ボディで 200 が返る（呼び出し側は黙って隠す）。

export interface RtkGainSummary {
  total_saved?: number;
  avg_savings_pct?: number;
  total_input?: number;
  total_output?: number;
  total_commands?: number;
}

export interface RtkGain {
  available?: boolean;
  error?: string;
  summary?: RtkGainSummary;
  daily?: { saved_tokens?: number }[];
}

export function fetchRtkGain(signal?: AbortSignal): Promise<RtkGain | { error: unknown }> {
  return api("api/agents/rtk/gain", signal ? { signal } : undefined);
}
