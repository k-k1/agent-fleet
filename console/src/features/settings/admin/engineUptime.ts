// Pure-function layer of the inference-engine status panel (ADR 0071).
//
// Keep the DOM and the api client out of here, for the reason uptime.ts states next door:
// `core/api/client.ts` touches localStorage at module init, so importing a single value from
// it breaks vitest under node. Import types only.
//
// Why this is not just uptime.ts. A workspace hour is "how many of this member's sessions were
// open"; an engine hour is "was the GPU box up, and was it answering anything". There are no
// members to break down and no session counts, but there are two states a workspace does not
// have — the cold start and the drain — that bill without serving. The grid, the colour levels
// and the timezone folding are shared (imported below); the meaning of a cell is not.
//
// This is not money (ADR 0048 決定 2). A cell says how long the hardware was up. Multiplying
// that by an hourly rate somebody typed in once is an estimate, and an estimate rendered as
// currency gets quoted back as a fact.

import { localBucket } from "../../usage/uptime.ts";

/** One hour as the CP returns it (GET /api/admin/engines/{key}/hourly). Zeroes are omitted
 *  from the wire. */
export type EngineHourPoint = {
  hour: string; // YYYY-MM-DDTHH (UTC)
  samples?: number;
  observed_secs?: number;
  running_secs?: number;
  starting_secs?: number;
  draining_secs?: number;
};

export type EngineUptimeResponse = {
  engine: string;
  from: string;
  to: string;
  interval_secs: number;
  /** ⚠️ Only the hours the controller actually ticked in. A MISSING hour is unknown, never
   *  stopped — there is no separate `observed` series here because the controller watches one
   *  engine and cannot half-observe it, so a row IS the observation. */
  hours: EngineHourPoint[];
};

/** What the colour of a cell stands for.
 *
 * - `running`: the engine was up and able to answer.
 * - `up`: a box existed and was billing — running plus the cold start plus the drain. The gap
 *   between the two is the money ADR 0071 決定 7 is about: a GPU takes 165-197 s to become
 *   useful and Managed Instances keeps the instance 427-477 s after the task is gone, and
 *   during neither of those is anything being served.
 */
export type EngineUptimeMetric = "running" | "up";

/** One cell, folded onto the viewer's local (day, hour). */
export type EngineCell = {
  day: string; // local YYYY-MM-DD
  hour: number; // local 0..23
  /** Whether the controller ticked in this hour. false = blank; grey does not mean "stopped". */
  observed: boolean;
  /** Denominator: the seconds the controller can actually vouch for.
   *
   * Comes straight from the CP and is never reconstructed as samples x interval — the
   * controller's interval drops to 5 s while an engine is starting, so the reconstruction
   * exceeds the hour in exactly the hours worth looking at. */
  observedSecs: number;
  runningSecs: number;
  startingSecs: number;
  drainingSecs: number;
};

const EMPTY_CELL = (day: string, hour: number): EngineCell => ({
  day,
  hour,
  observed: false,
  observedSecs: 0,
  runningSecs: 0,
  startingSecs: 0,
  drainingSecs: 0,
});

/** Folds the response into a local-time grid keyed by `day|hour`.
 *
 * `observed` is set by the presence of an hour in the response and by nothing else. Inferring
 * it from "running_secs > 0" would paint every hour the CP was down in the same grey as an
 * hour the engine was deliberately asleep, and an operator would read a control-plane outage
 * as a quiet night. */
export function buildEngineGrid(res: EngineUptimeResponse | null): Map<string, EngineCell> {
  const grid = new Map<string, EngineCell>();
  if (!res) return grid;
  for (const h of res.hours || []) {
    const b = localBucket(h.hour);
    if (!b) continue;
    const k = b.day + "|" + b.hour;
    let c = grid.get(k);
    if (!c) {
      c = EMPTY_CELL(b.day, b.hour);
      grid.set(k, c);
    }
    // Accumulated rather than assigned: at a timezone offset that is a whole number of hours
    // this is one UTC hour per cell, but the fold is by local hour and nothing here should
    // depend on that staying true.
    c.observed = true;
    c.observedSecs += h.observed_secs || 0;
    c.runningSecs += h.running_secs || 0;
    c.startingSecs += h.starting_secs || 0;
    c.drainingSecs += h.draining_secs || 0;
  }
  return grid;
}

/** Seconds a cell counts towards the chosen metric. */
export function engineMetricSecs(c: EngineCell | undefined, metric: EngineUptimeMetric): number {
  if (!c) return 0;
  return metric === "running" ? c.runningSecs : c.runningSecs + c.startingSecs + c.drainingSecs;
}

/** Value of a cell: the fraction of the OBSERVED time, 0..1.
 *
 * Observed time, not 3600 seconds. The hour still in progress and any hour the CP was restarted
 * part way through would otherwise fade towards zero, and the colour would be showing gaps in
 * observation rather than uptime.
 *
 * Capped at 1. A sample can be recorded a fraction of a second past an hour boundary, and a
 * cell reading "104% up" destroys confidence in a panel whose whole job is to be believed. */
export function engineCellValue(c: EngineCell | undefined, metric: EngineUptimeMetric): number {
  if (!c || c.observedSecs <= 0) return 0;
  return Math.min(1, engineMetricSecs(c, metric) / c.observedSecs);
}

/** The three states a cell can be in, which must never collapse to two.
 *
 * `unobserved` is not a kind of `stopped`. It means the control plane was not watching — it was
 * down, or the engine did not exist yet — and drawing it as stopped turns an outage into a
 * confident claim that the hardware was idle. */
export function engineCellState(
  c: EngineCell | undefined,
  metric: EngineUptimeMetric,
): "unobserved" | "stopped" | "running" {
  if (!c?.observed) return "unobserved";
  return engineMetricSecs(c, metric) > 0 ? "running" : "stopped";
}

/** Splits seconds into whole hours and minutes for display.
 *
 * Anything above zero rounds up to at least one minute: a 40-second start that displayed as
 * "0 minutes" reads as "it never came up", which is the opposite of what happened. */
export function splitHM(secs: number): { hours: number; mins: number } {
  if (secs <= 0) return { hours: 0, mins: 0 };
  const total = Math.max(1, Math.round(secs / 60));
  return { hours: Math.floor(total / 60), mins: total % 60 };
}

/** Seconds between now and an RFC3339 instant, positive when it is in the future. Returns null
 *  for an absent or unparseable value, which every caller renders as "no answer" rather than
 *  as zero — a countdown showing 0 is a claim that it is happening right now. */
export function secsUntil(at: string | undefined, now: number): number | null {
  if (!at) return null;
  const t = Date.parse(at);
  if (Number.isNaN(t)) return null;
  return Math.round((t - now) / 1000);
}

/** Whether the demand counter has been running for less than its own window.
 *
 * ⚠️ This is the guard against the panel's one available lie. The rolling count lives in the
 * CP's memory and nowhere else, so a control plane replaced two minutes ago reports "0 requests
 * in the last 5 minutes" while somebody is mid-conversation with the engine. When this is true
 * the count must be shown as partial — the last-demand timestamp beside it is persisted and
 * stays true regardless. */
export function windowIsPartial(row: { window_secs?: number; window_counted_secs?: number }): boolean {
  const w = row.window_secs || 0;
  const counted = row.window_counted_secs;
  if (w <= 0 || counted === undefined) return false;
  // A second of slack: the counter is started a hair after the window is read, and a panel that
  // permanently declares itself partial by one second teaches the reader to ignore the warning.
  return counted < w - 1;
}
