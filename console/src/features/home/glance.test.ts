import { describe, expect, it } from "vitest";
import { t } from "../../lib/i18n/index.ts";
import { recordUsageReading, readingsFor, useUsageReadings } from "../../app/wsbar/usageReadings.ts";
import type { StatSample } from "../../core/store/wsStatsFeed.ts";
import type { UsageSeries } from "../usage/api.ts";
import { limitRows, limitWindow, machineSummary, modelUsage, uptimeSummary } from "./glance.ts";

const HOUR = 3600000;
const NOW = Date.parse("2026-11-02T12:00:00Z");
const iso = (ms: number) => new Date(ms).toISOString();

describe("limitWindow", () => {
  it("gives a live window its figure", () => {
    expect(limitWindow("5h", { pct: 41.6, resetsAt: iso(NOW + HOUR) }, NOW)).toMatchObject({ pct: 42, note: "" });
  });
  it("shows no figure for a stale window, never 0", () => {
    expect(limitWindow("5h", { pct: 0, resetsAt: iso(NOW + HOUR), stale: true }, NOW)).toMatchObject({ pct: null, note: "stale" });
  });
  it("reads 'reset not yet observed' once the reset time has passed", () => {
    expect(limitWindow("5h", { pct: 97, resetsAt: iso(NOW - 1) }, NOW)).toMatchObject({ pct: null, note: "reset_not_observed" });
  });
});

describe("limitRows", () => {
  const stored = (reading: object, at = NOW) => ({ reading, at });
  it("lists only agents that have a reading, with the reading's age dated to now", () => {
    const rows = limitRows(
      {
        claude: stored({ ok: true, ageSec: 30, fiveHour: { pct: 10, resetsAt: iso(NOW + HOUR) }, sevenDay: { pct: 20, resetsAt: iso(NOW + 9 * HOUR) } }, NOW - 60000),
        codex: stored({ ok: true, authed: true }),
      },
      NOW,
      t,
    );
    expect(rows.map((r) => r.kind)).toEqual(["claude"]);
    expect(rows[0].windows.map((w) => w.pct)).toEqual([10, 20]);
    expect(rows[0].ageSec).toBe(90);
  });
});

describe("recordUsageReading", () => {
  it("keeps the last good reading across a failed read, and drops another tenant's", () => {
    useUsageReadings.setState({ tenant: "", byKind: {} });
    recordUsageReading("a", "claude", { ok: true, fiveHour: { pct: 5, resetsAt: "x" } }, 1);
    recordUsageReading("a", "claude", { ok: false, authed: true }, 2);
    expect(readingsFor(useUsageReadings.getState(), "a").claude.reading.ok).toBe(true);
    expect(readingsFor(useUsageReadings.getState(), "b")).toEqual({});
    recordUsageReading("b", "claude", { ok: false, authed: true }, 3);
    expect(readingsFor(useUsageReadings.getState(), "a")).toEqual({});
    expect(readingsFor(useUsageReadings.getState(), "b").claude.at).toBe(3);
  });
});

const sample = (o: Partial<StatSample>): StatSample => ({
  t: 0, cpu: null, memUsed: null, memMax: null, diskUsed: null, diskTotal: null, oom: false, memPressure: null, ...o,
});

describe("machineSummary", () => {
  it("is 'none' with no sample and 'gap' for an unmeasured tick (null, not 0)", () => {
    expect(machineSummary([]).state).toBe("none");
    const g = machineSummary([sample({})]);
    expect(g).toMatchObject({ state: "gap", cpu: null, memUsed: null });
  });
  it("carries measured values only and remembers an OOM seen earlier in the window", () => {
    const m = machineSummary([sample({ oom: true, cpu: 1 }), sample({ cpu: 40, memUsed: 5, memMax: 10 })]);
    expect(m).toMatchObject({ state: "ok", cpu: 40, memUsed: 5, memMax: 10, oom: true });
  });
});

describe("uptimeSummary", () => {
  const hour = (h: string, o: object) => ({ hour: h, ...o });
  it("keeps three day states: unobserved is not stopped", () => {
    const today = new Date(NOW);
    const key = (d: Date) => d.toISOString().slice(0, 13);
    const res = {
      from: "", to: "", interval_secs: 60,
      observed: [hour(key(today), { samples: 60 }), hour(key(new Date(NOW - 24 * HOUR)), { samples: 60 })],
      members: [{ tenant: "", user_key: "u", email: "", hours: [hour(key(today), { samples: 60, running_secs: 1800, measured_secs: 1800, session_secs: 0 })] }],
    };
    const s = uptimeSummary(res, NOW);
    expect(s.days).toHaveLength(7);
    const states = s.days.map((d) => d.state);
    expect(states.filter((x) => x === "unobserved").length).toBe(5);
    expect(states.filter((x) => x === "stopped").length).toBe(1);
    expect(states[6]).toBe("running");
    expect(s.todaySecs).toBe(1800);
    expect(s.weekSecs).toBe(1800);
    expect(s.idleRunningSecs).toBe(1800);
    expect(s.observed).toBe(true);
  });
  it("is not observed when nothing came back", () => {
    expect(uptimeSummary(null, NOW).observed).toBe(false);
  });
});

const agg = (spend: number, calls = 1, cost = 0) => ({ spend, in: 0, out: 0, cread: 0, ccreate: 0, calls, cost_est_usd: cost });
const series = (by: Record<string, number>, o: Partial<UsageSeries> = {}): UsageSeries => ({
  from: "", to: "", bucket: "hour", by: "model",
  buckets: [{ t: "x", series: Object.fromEntries(Object.entries(by).map(([k, v]) => [k, agg(v)])) }],
  totals: agg(Object.values(by).reduce((a, b) => a + b, 0), 5),
  coverage: {}, unmeasured_calls: 0, ...o,
});

describe("modelUsage", () => {
  it("shows the top three and folds the rest into other", () => {
    const m = modelUsage(series({ a: 100, b: 80, c: 60, d: 30, e: 10 }));
    expect(m.rows.map((r) => r.key)).toEqual(["a", "b", "c"]);
    expect(m.other).toEqual({ count: 2, spend: 40 });
    expect(m.state).toBe("measured");
  });
  it("treats calls without tokens as unmeasured, never as zero spend", () => {
    const m = modelUsage(series({}, { totals: agg(0, 4), unmeasured_calls: 4 }));
    expect(m.state).toBe("unmeasured");
    expect(m.rows).toEqual([]);
  });
  it("is 'nothing' only when there was no call at all", () => {
    expect(modelUsage(series({}, { totals: agg(0, 0) })).state).toBe("nothing");
  });
});
