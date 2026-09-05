// features/usage/api — types and fetch for GET /api/usage/series (docs/log/46 §4-a / ADR0029).
//
// The server aggregates and returns the result; raw logs never reach the Console. The wire shape
// maps one-to-one onto usageSeriesResp in workspace/agent/usage_series.go. The server rejects an
// unknown axis with 400, so this side keeps the vocabulary closed in UsageDim.
import { api } from "../../core/api/client.ts";

/** Aggregation axis. Same vocabulary as the server's validUsageDim; change both sides together. */
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

/** One entry of `series`. spend = in + ccreate + out (cache_read is not counted). */
export interface UsageAgg {
  spend: number;
  in: number;
  out: number;
  cread: number;
  ccreate: number;
  calls: number;
  /** Measured cost. Only claude's auxiliary calls report it; the session itself does not. */
  cost_usd?: number;
  /**
   * Estimated API-equivalent amount, derived by the server from its price table times the token
   * counts (usage_price.go). It is a different value from the measured cost_usd and must never be
   * added to it: one number never mixes two ways of measuring. A model missing from the price
   * table has no value here rather than 0; priced_spend / unpriced_spend say how much could be
   * priced.
   */
  cost_est_usd?: number;
}

/**
 * The effective unit price used for the amounts in this response ($ per million tokens) and where
 * it came from: src is "builtin" (the built-in table) or "catalog:<provider>/<model>". An amount
 * without its source cannot be checked, so the server attaches this as model name to price.
 */
export interface UsagePrice {
  src: string;
  in: number;
  out: number;
  cread: number;
  cwrite: number;
  /** The same model name is priced differently per kind; the larger consumer is displayed. */
  ambiguous?: boolean;
}

/** What the price catalogue (models.dev) declares. Absent entirely when there is none. */
export interface UsageCatalog {
  origin: string; // opencode | file | env
  models: number;
  fetched?: string;
}

export interface UsageBucket {
  t: string; // start of the bucket (RFC3339, UTC)
  series: Record<string, UsageAgg>;
}

/** How much this kind reports about itself, derived automatically from the data. */
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
  /** Only when split is given (e.g. feature x model). matrix[by][split] = the aggregate. */
  matrix?: Record<string, Record<string, UsageAgg>>;
  coverage: Record<string, UsageCoverage>;
  unmeasured_calls: number;
  /** Consumption that could / could not be priced from the table, in spend terms. */
  priced_spend?: number;
  unpriced_spend?: number;
  /** Model name to the unit price used in this response. */
  prices?: Record<string, UsagePrice>;
  catalog?: UsageCatalog;
  /** Part of the requested range predates raw retention and could not be rebuilt at hour grain. */
  truncated?: boolean;
  /**
   * Folding of the sessions themselves into the ledger was running when this read happened, so
   * the response may not yet include the most recent turns. Folding is asynchronous, so while
   * this is set UsageView refetches until it settles.
   */
  folding?: boolean;
}

export interface UsageQuery {
  from?: string;
  to?: string;
  bucket?: "day" | "hour";
  by?: UsageDim;
  split?: UsageDim;
  /** OR within an axis, AND across axes. Only a trailing * is a prefix match
   * (e.g. kind:claude,feature:title.*). */
  filter?: string;
  include?: string;
  /**
   * "force" skips the 60-second throttle on session folding. Set it only on an explicit refetch:
   * on an automatic one, every finished fold starts the next and folding never stops.
   */
  fold?: "force";
}

export function usageSeriesPath(q: UsageQuery): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) if (v) p.set(k, String(v));
  const qs = p.toString();
  return "api/usage/series" + (qs ? "?" + qs : "");
}

/** Fetch one series. api() resolves with {error} instead of throwing, so the caller must check. */
export function fetchUsageSeries(q: UsageQuery, signal?: AbortSignal): Promise<UsageSeries | { error: unknown }> {
  return api(usageSeriesPath(q), signal ? { signal } : undefined);
}

// --- rtk savings ("rtk 効果") -------------------------------------------------
// A separate lineage from the ledger above (which the CP aggregates): the Agent reads rtk's own
// history DB inside the container with `rtk gain --all --format json` and passes it through, so
// the schema is rtk's (workspace/agent/agent_rtk.go handleAgentRTKGain). A missing rtk or a
// failure still returns 200 with a soft body carrying available/error, which the caller hides
// silently. An older Agent (from the --daily era) returns only daily, so read weekly/monthly as
// possibly absent.

/** One bucket. The date key depends on the grain: daily=date / weekly=week_start,week_end /
 * monthly=month. */
export interface RtkGainBucket {
  date?: string; // "YYYY-MM-DD" (a bare date, no timezone)
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
