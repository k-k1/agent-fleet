// What the "At a glance" section on Home computes (#2362) — pure functions only (no store, no
// clock, no api client), so the node vitest project pins them. The numbers come from the same
// sources as the detailed views; this file only picks and shapes them, and carries the rules
// those views hold: zero is not unmeasured, stale is stated, a blank hour is not a stopped one.
import type { MsgKey, t } from "../../lib/i18n/index.ts";
import type { StoredReading } from "../../app/wsbar/usageReadings.ts";
import type { UsageReading, UsageWindow } from "../../app/wsbar/usageCommon.tsx";
import { REMAINING_SOURCES, USAGE_SOURCES } from "../../app/wsbar/usageSources.ts";
import type { StatSample } from "../../core/store/wsStatsFeed.ts";
import type { UsageSeries } from "../usage/api.ts";
import { keysByMagnitude, metricOf, totalsByKey } from "../usage/series.ts";
import { buildGrid, cellLevel, localDayKey, type UptimeResponse } from "../usage/uptime.ts";

// --- Limits -----------------------------------------------------------------------------

export type LimitNote = "" | "stale" | "reset_not_observed";

export interface LimitWindowModel {
  label: string;
  /** null = no believable figure ("—"); never 0 standing in for "unknown". */
  pct: number | null;
  resetsAt?: string;
  note: LimitNote;
}

export interface LimitRowModel {
  kind: string;
  windows: LimitWindowModel[];
  /** Seconds since the provider's observation, dated to `now`; null when the reading has no age. */
  ageSec: number | null;
  /** Codex / muse / agy readings are snapshots from the last turn, not live queries. */
  live: boolean;
  manageURL?: string;
}

/** One window as the chip's popover would read it. A window flagged stale by the Agent, or whose
 *  reset time has passed (so the % describes a window that has rolled over), has no figure. */
export function limitWindow(label: string, w: Pick<UsageWindow, "pct" | "stale"> & { resetsAt?: string }, now: number): LimitWindowModel {
  if (w.stale) return { label, pct: null, resetsAt: w.resetsAt, note: "stale" };
  const reset = w.resetsAt ? Date.parse(w.resetsAt) : NaN;
  if (!Number.isNaN(reset) && now >= reset) return { label, pct: null, resetsAt: w.resetsAt, note: "reset_not_observed" };
  return { label, pct: Math.round(w.pct), resetsAt: w.resetsAt, note: "" };
}

const ageOf = (s: StoredReading, now: number): number | null =>
  typeof s.reading.ageSec === "number" ? s.reading.ageSec + Math.max(0, (now - s.at) / 1000) : null;

/** Every agent that has a reading, in the WS bar's order. An agent without one is left out
 *  rather than drawn as zero. */
export function limitRows(byKind: Record<string, StoredReading>, now: number, tr: typeof t): LimitRowModel[] {
  const rows: LimitRowModel[] = [];
  for (const src of USAGE_SOURCES) {
    const s = byKind[src.kind];
    const u: UsageReading | undefined = s?.reading;
    if (!s || !u) continue;
    const windows = [
      u.fiveHour && limitWindow(tr(src.fiveLabelKey as MsgKey), u.fiveHour, now),
      u.sevenDay && limitWindow(tr(src.weekLabelKey as MsgKey), u.sevenDay, now),
    ].filter((w): w is LimitWindowModel => !!w);
    if (!windows.length) continue;
    rows.push({ kind: src.kind, windows, ageSec: ageOf(s, now), live: src.live, manageURL: src.manageURL });
  }
  for (const src of REMAINING_SOURCES) {
    const s = byKind[src.kind];
    if (!s || !s.reading.ok) continue;
    const windows = src
      .pools(s.reading, tr)
      .map((p) => limitWindow(p.label, { pct: p.pct, resetsAt: p.resetsAt, stale: p.stale }, now));
    if (!windows.length) continue;
    rows.push({ kind: src.kind, windows, ageSec: ageOf(s, now), live: false, manageURL: src.manageURL });
  }
  return rows;
}

// --- Machine ----------------------------------------------------------------------------

export interface MachineSummary {
  /** none = the feed has no sample yet; gap = the last tick could not be established (stream
   *  down or workspace not reporting); ok = a measured reading. */
  state: "none" | "gap" | "ok";
  cpu: number | null;
  memUsed: number | null;
  memMax: number | null;
  diskUsed: number | null;
  diskTotal: number | null;
  oom: boolean;
}

/** The last sample, plus whether an OOM kill shows anywhere in the window the feed still holds
 *  (the flag itself expires; "it happened here" should not). Only measured fields are carried —
 *  nothing is filled from configuration (lib/machine.ts keeps those apart). */
export function machineSummary(samples: StatSample[]): MachineSummary {
  const last = samples[samples.length - 1];
  const oom = samples.some((s) => s.oom);
  if (!last) return { state: "none", cpu: null, memUsed: null, memMax: null, diskUsed: null, diskTotal: null, oom };
  const measured = last.cpu != null || last.memUsed != null || last.diskUsed != null;
  return {
    state: measured ? "ok" : "gap",
    cpu: last.cpu,
    memUsed: last.memUsed,
    memMax: last.memMax,
    diskUsed: last.diskUsed,
    diskTotal: last.diskTotal,
    oom,
  };
}

// --- Uptime -----------------------------------------------------------------------------

export type DayState = "unobserved" | "stopped" | "running";

export interface UptimeDay {
  day: string;
  state: DayState;
  runningSecs: number;
  /** Heatmap colour level 1..5 for a running day; 0 otherwise. */
  level: number;
}

export interface UptimeSummary {
  days: UptimeDay[];
  todaySecs: number;
  weekSecs: number;
  /** Running time in hours where the Agent was reachable and no session existed. */
  idleRunningSecs: number;
  /** Whether the sampler observed anything in the 7 days; false = nothing to show. */
  observed: boolean;
}

export const UPTIME_DAYS = 7;

/** The last `n` local calendar days ending today, oldest first. Built with calendar arithmetic
 *  (setDate semantics), never `now - k * 86400000`: across a DST change a local day is 23 or 25
 *  hours long, and millisecond steps repeat or skip a date there. */
export function lastLocalDays(now: number, n: number): string[] {
  const d = new Date(now);
  const out: string[] = [];
  for (let i = n - 1; i >= 0; i--) out.push(localDayKey(new Date(d.getFullYear(), d.getMonth(), d.getDate() - i)));
  return out;
}

/** The /api/usage/me/hourly window for those days: one calendar day wider at each end, because
 *  the server cuts on UTC days and the local edge cells need the neighbours. */
export function uptimeWindow(now: number): { from: string; to: string } {
  const days = lastLocalDays(now, UPTIME_DAYS);
  const [fy, fm, fd] = days[0].split("-").map(Number);
  const [ty, tm, td] = days[days.length - 1].split("-").map(Number);
  return { from: localDayKey(new Date(fy, fm - 1, fd - 1)), to: localDayKey(new Date(ty, tm - 1, td + 1)) };
}

/** The last seven local days. A day with no observed hour is "unobserved" (the CP was not
 *  watching), which is not "stopped": the heatmap's third state survives here. */
export function uptimeSummary(res: UptimeResponse | null, now: number): UptimeSummary {
  const dayKeys = lastLocalDays(now, UPTIME_DAYS);
  const from = dayKeys[0];
  const to = dayKeys[dayKeys.length - 1];
  const grid = buildGrid(res);
  const days: UptimeDay[] = dayKeys.map((day) => {
    let running = 0;
    let observed = false;
    for (let h = 0; h < 24; h++) {
      const c = grid.get(day + "|" + h);
      if (!c) continue;
      running += c.runningSecs;
      if (c.observed || c.runningSecs > 0) observed = true;
    }
    return { day, state: !observed ? "unobserved" : running > 0 ? "running" : "stopped", runningSecs: running, level: 0 };
  });
  const max = Math.max(0, ...days.map((d) => d.runningSecs));
  for (const d of days) if (d.state === "running") d.level = cellLevel(d.runningSecs, max, false);
  let idle = 0;
  for (const c of grid.values()) {
    if (c.day < from || c.day > to) continue;
    if (c.runningSecs > 0 && c.measuredSecs > 0 && c.sessionSecs <= 0) idle += c.runningSecs;
  }
  return {
    days,
    todaySecs: days.find((d) => d.day === to)?.runningSecs ?? 0,
    weekSecs: days.reduce((a, d) => a + d.runningSecs, 0),
    idleRunningSecs: idle,
    observed: days.some((d) => d.state !== "unobserved"),
  };
}

// --- Model usage ------------------------------------------------------------------------

export interface ModelUsage {
  rows: { key: string; spend: number; share: number }[];
  /** What the folded-away models add up to; 0 when there is no remainder. */
  other: { count: number; spend: number };
  spend: number;
  calls: number;
  unmeasured: number;
  /** Estimated API-equivalent amount; 0 = none (render "—", never "≈$0"). */
  costEst: number;
  /** nothing = no call at all; unmeasured = calls exist but none reported tokens (so "—", not
   *  0); measured = a figure to show. */
  state: "nothing" | "unmeasured" | "measured";
  folding: boolean;
}

export const MODEL_ROWS = 3;

export function modelUsage(s: UsageSeries): ModelUsage {
  const totals = totalsByKey(s.buckets || []);
  const keys = keysByMagnitude(totals, "spend");
  const spend = s.totals?.spend || 0;
  const calls = s.totals?.calls || 0;
  const unmeasured = s.unmeasured_calls || 0;
  const rows = keys
    .slice(0, MODEL_ROWS)
    .map((key) => ({ key, spend: metricOf(totals.get(key), "spend"), share: 0 }))
    .filter((r) => r.spend > 0);
  const rest = keys.slice(rows.length);
  const restSpend = rest.reduce((a, k) => a + metricOf(totals.get(k), "spend"), 0);
  for (const r of rows) r.share = spend > 0 ? r.spend / spend : 0;
  return {
    rows,
    other: { count: restSpend > 0 ? rest.length : 0, spend: restSpend },
    spend,
    calls,
    unmeasured,
    costEst: s.totals?.cost_est_usd || 0,
    state: spend > 0 ? "measured" : calls > 0 || unmeasured > 0 ? "unmeasured" : "nothing",
    folding: !!s.folding,
  };
}
