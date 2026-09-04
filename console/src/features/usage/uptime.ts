// Pure-function layer of the uptime heatmap (docs/log/83). 24 hours down, dates across.
//
// Keep the DOM and the api client out of here. `core/api/client.ts` touches localStorage at
// module init, so importing a single value from it breaks vitest under node (the same trap as
// series.ts in the usage chart). Import types only.
//
// This is not money. Real cost is only available per day, and an hourly figure could only be an
// estimate (ADR 0048 decision 2). A cell shows occupancy and session counts; colour never means
// a price.

/** One hour for one member as the CP returns it (GET /api/usage/me/hourly and friends). Zeroes
 *  are omitted from the wire. */
export type UptimePoint = {
  hour: string; // YYYY-MM-DDTHH (UTC)
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
  /** The hours (UTC) the sampler was running, and how many times it looked in each. An hour
   * missing here means "unknown", not "stopped". samples is also the denominator of a cell. */
  observed: UptimePoint[];
  members: UptimeMember[];
};

/** Metric shown in a cell. One metric per cell — never encode a second one alongside colour. */
export type UptimeMetric = "running" | "sessions" | "busy";

/** One member's contribution, for the hover breakdown. */
export type UptimeContribution = {
  label: string;
  runningSecs: number;
  sessionSecs: number;
  busySecs: number;
};

/** One cell, folded onto the local-time (day, hour). */
export type UptimeCell = {
  day: string; // local YYYY-MM-DD
  hour: number; // local 0..23
  /** Whether the sampler observed this hour. false = blank; grey does not mean "stopped". */
  observed: boolean;
  /** Denominator of the cell: the seconds the sampler actually observed this hour (heartbeat
   * samples x interval).
   *
   * Never hard-code an hour as 3600 seconds. The current, still-incomplete hour and any hour the
   * CP was down would both fade, so colour would show missing observation rather than uptime.
   * It is not the sum of the members' samples either: that would make an aggregate cell show
   * "what fraction of workspaces were running" instead of "how many ran on average". */
  possibleSecs: number;
  /** Fallback denominator used only for hours whose heartbeat was missed (max samples across
   *  the member rows). */
  fallbackSecs: number;
  runningSecs: number;
  measuredSecs: number;
  sessionSecs: number;
  busySecs: number;
  maxSessions: number;
  maxBusy: number;
  contributions: UptimeContribution[];
};

/** Maps a UTC hour-bucket key onto the viewer's local (day, hour).
 *
 * The server only ever buckets in UTC. Without this shift a Japanese user's heatmap is off by
 * nine hours and claims they work every night after midnight.
 * With a 30-minute offset such as +05:30 one UTC hour straddles a local hour boundary; it is
 * rounded to the hour its start falls in, and the UI shows a note.
 */
export function localBucket(utcHour: string): { day: string; hour: number } | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2})$/.exec(utcHour);
  if (!m) return null;
  const d = new Date(Date.UTC(+m[1], +m[2] - 1, +m[3], +m[4]));
  if (Number.isNaN(d.getTime())) return null;
  return { day: localDayKey(d), hour: d.getHours() };
}

/** Local YYYY-MM-DD. Do not use toISOString here — it converts back to UTC. */
export function localDayKey(d: Date): string {
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
}

/** Local dates from `from` to `to`, both ends inclusive. */
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

/** One day before `from` and one after `to`. The server cuts on UTC days, so filling the local
 *  edge cells needs an extra day at each end (at +09:00, UTC 15:00 is midnight the next day). */
export function widenForTimezone(from: string, to: string): { from: string; to: string } {
  const shift = (day: string, days: number) =>
    localDayKey(new Date(new Date(day + "T00:00:00").getTime() + days * 86400000));
  return { from: shift(from, -1), to: shift(to, 1) };
}

/** Display name for a member: user_key, else email, else empty (which reads as unknown). */
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

/** Folds the response into a local-time grid keyed by `day|hour`.
 *
 * `observed` comes from the heartbeat alone. Inferring it from the presence of member rows would
 * paint an hour the CP was down the same grey as an hour nobody worked.
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
      // Guard for hours that show activity but have no heartbeat: dividing by a zero denominator
      // feeds Infinity straight into the level calculation. Use this row's sample count as a floor.
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
    // Largest contribution first: the hover shows only the top three, so the order carries meaning.
    c.contributions.sort((a, b) => b.sessionSecs - a.sessionSecs || b.runningSecs - a.runningSecs);
  }
  return grid;
}

/** Value of a cell, one per metric.
 *
 * - running: the fraction of the observed time that was running. For a single member that is a
 *   0..1 utilisation; aggregated it is "how many ran on average" — the denominator covers one
 *   hour either way, so the same formula gives both.
 * - sessions / busy: average concurrency while running. The denominator is measured, not
 *   running: counting time that never reached the Agent would dilute the busy hours.
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

/** Whether the session count is unknown for this hour (it was up, but the Agent was unreachable).
 *  Must not look the same as zero sessions. */
export function isUnmeasured(c: UptimeCell | undefined, metric: UptimeMetric): boolean {
  return metric !== "running" && !!c && c.runningSecs > 0 && c.measuredSecs <= 0;
}

/** Colour level, 1..5.
 *
 * There are only five levels, so 0 is treated differently per metric. For the count metrics,
 * "up but zero sessions" is the lowest level (pale orange) and is distinct from grey (stopped).
 * For the running metric a 0 cell is not drawn at all (it means stopped), so all five levels are
 * available.
 */
export function cellLevel(value: number, max: number, zeroFloor: boolean): number {
  if (max <= 0) return 1;
  const v = Math.max(0, Math.min(1, value / max));
  if (zeroFloor) return v <= 0 ? 1 : Math.min(5, 1 + Math.ceil(v * 4));
  return Math.min(5, Math.max(1, Math.ceil(v * 5)));
}

/** Maximum the legend and the level split are scaled against. A max of 0 would put every cell on
 *  level 1, so the caller guards that in the level calculation; this returns the plain maximum. */
export function maxValue(grid: Map<string, UptimeCell>, days: string[], metric: UptimeMetric): number {
  let max = 0;
  for (const day of days) {
    for (let h = 0; h < 24; h++) {
      max = Math.max(max, cellValue(grid.get(day + "|" + h), metric));
    }
  }
  return max;
}

/** Value band a legend swatch stands for: the range level n covers, for display. */
export function levelBand(level: number, max: number, zeroFloor: boolean): [number, number] {
  if (zeroFloor) {
    if (level <= 1) return [0, 0];
    const step = max / 4;
    return [(level - 2) * step, (level - 1) * step];
  }
  const step = max / 5;
  return [(level - 1) * step, level * step];
}

/** Seconds to the minute count used by the short "1h 2m" form. Anything non-zero rounds up to at
 *  least 1, so a short run is never shown as 0 minutes and read as "not running". */
export function minutesOf(secs: number): number {
  return secs > 0 ? Math.max(1, Math.round(secs / 60)) : 0;
}

/** Whether the local timezone aligns to hour boundaries. If it does not, cells are rounded with a
 *  30-minute skew and the UI has to say so. */
export function timezoneAlignsToHours(d: Date = new Date()): boolean {
  return d.getTimezoneOffset() % 60 === 0;
}
