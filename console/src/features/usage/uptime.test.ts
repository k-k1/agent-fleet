// Pure functions of the uptime heatmap (docs/log/83).
//
// The timezone is pinned in this file. Left at the default, CI and a developer machine disagree
// and the very point of the feature — that dates shift at +09:00 — goes unchecked. Node picks up
// a later change to process.env.TZ (measured).
//
// Pin it in the module body; `beforeAll` is too late. A describe callback runs at collection
// time, before beforeAll, so calling `buildGrid` there folds against the machine's own clock:
// green on a JST machine, red only on CI (UTC). Grid construction is in beforeAll for the same
// reason.
import { afterAll, beforeAll, describe, expect, it } from "vitest";
import {
  buildGrid,
  cellLevel,
  cellValue,
  dayRange,
  isUnmeasured,
  levelBand,
  localBucket,
  maxValue,
  minutesOf,
  widenForTimezone,
} from "./uptime.ts";
import type { UptimeResponse } from "./uptime.ts";

const ORIGINAL_TZ = process.env.TZ;
process.env.TZ = "Asia/Tokyo";
afterAll(() => {
  process.env.TZ = ORIGINAL_TZ;
});

describe("UTC to local folding", () => {
  // Get this wrong and a Japanese user's heatmap is off by nine hours, claiming they work every
  // night after midnight.
  it("carries a UTC hour bucket to the viewer's local time", () => {
    expect(localBucket("2026-09-01T00")).toEqual({ day: "2026-09-01", hour: 9 });
    // The side that crosses the date: UTC 15:00 is local midnight the next day.
    expect(localBucket("2026-09-01T15")).toEqual({ day: "2026-09-02", hour: 0 });
  });

  it("drops a malformed key instead of throwing", () => {
    expect(localBucket("")).toBeNull();
    expect(localBucket("2026-09-01")).toBeNull();
  });

  // Without fetching the days either side, the edge columns come out half empty.
  it("widens the requested window by a day at each end", () => {
    expect(widenForTimezone("2026-09-01", "2026-09-10")).toEqual({
      from: "2026-08-31",
      to: "2026-09-11",
    });
  });

  it("includes both ends of the date column range", () => {
    expect(dayRange("2026-08-30", "2026-09-02")).toEqual([
      "2026-08-30",
      "2026-08-31",
      "2026-09-01",
      "2026-09-02",
    ]);
  });
});

const RES: UptimeResponse = {
  from: "2026-09-01",
  to: "2026-09-01",
  interval_secs: 300,
  // UTC hour 00 = local hour 9. samples is the cell denominator (seconds observed).
  observed: [
    { hour: "2026-09-01T00", samples: 12 },
    { hour: "2026-09-01T01", samples: 12 },
  ],
  members: [
    {
      tenant: "sales",
      user_key: "aoi",
      email: "aoi@example.com",
      hours: [
        {
          hour: "2026-09-01T00",
          samples: 12,
          running_secs: 3600,
          measured_secs: 3600,
          session_secs: 7200,
          busy_secs: 1800,
          max_sessions: 3,
          max_busy: 1,
        },
      ],
    },
    {
      tenant: "sales",
      user_key: "bun",
      email: "bun@example.com",
      hours: [
        {
          hour: "2026-09-01T00",
          samples: 12,
          running_secs: 1800,
          measured_secs: 1800,
          session_secs: 1800,
          busy_secs: 0,
          max_sessions: 1,
          max_busy: 0,
        },
        // Up, but the Agent was unreachable: no measured_secs.
        { hour: "2026-09-01T01", samples: 12, running_secs: 3600, max_sessions: 0 },
      ],
    },
  ],
};

describe("grid construction", () => {
  // Do not fold in the describe body (collection time): running before the timezone pin above
  // folds against the machine's clock and is green only on a JST machine.
  let grid: Map<string, ReturnType<typeof buildGrid> extends Map<string, infer C> ? C : never>;
  beforeAll(() => {
    grid = buildGrid(RES);
  });

  it("stacks the members' contributions into one cell", () => {
    const c = grid.get("2026-09-01|9")!;
    expect(c.runningSecs).toBe(5400);
    expect(c.sessionSecs).toBe(9000);
    // The denominator is one heartbeat's worth (the hour itself). Summing the members' samples
    // would turn an aggregate cell into "what fraction was running" instead of "how many on
    // average".
    expect(c.possibleSecs).toBe(12 * 300);
    // Peaks are not summed: 3, the most seen at once, not the total of 4.
    expect(c.maxSessions).toBe(3);
  });

  // observed comes from the heartbeat alone. Inferring it from the member rows would paint an
  // hour the CP was down the same grey as an hour nobody worked.
  it("derives observed hours from the heartbeat, independent of the member rows", () => {
    expect(grid.get("2026-09-01|9")!.observed).toBe(true);
    expect(grid.get("2026-09-01|10")!.observed).toBe(true);
    // An hour without a heartbeat has no row at all, i.e. it is blank.
    expect(grid.get("2026-09-01|11")).toBeUndefined();
  });

  it("orders the breakdown by contribution, since the hover shows only the top three", () => {
    expect(grid.get("2026-09-01|9")!.contributions.map((c) => c.label)).toEqual(["aoi", "bun"]);
  });

  it("divides average concurrency by measured, not running", () => {
    // 5400 running seconds and 9000 session seconds gives an average of 1.67 sessions.
    expect(cellValue(grid.get("2026-09-01|9"), "sessions")).toBeCloseTo(9000 / 5400, 5);
    expect(cellValue(grid.get("2026-09-01|9"), "busy")).toBeCloseTo(1800 / 5400, 5);
  });

  // Because the denominator covers exactly one hour, the same formula is a utilisation for one
  // member and a count when aggregated. Summing the members' samples turns the aggregate into
  // "what fraction was running".
  it("reads running as the average number up in that hour (0..1 for one member)", () => {
    // 5400 s / 3600 s (12 samples x 300 s) = 1.5 on average
    expect(cellValue(grid.get("2026-09-01|9"), "running")).toBeCloseTo(1.5, 5);
  });

  // Never divide by zero for an hour whose heartbeat was missed: Infinity flows straight into the
  // level calculation.
  it("gives an hour with activity but no heartbeat a denominator", () => {
    const g = buildGrid({
      ...RES,
      observed: [],
      members: [
        {
          tenant: "sales",
          user_key: "aoi",
          email: "",
          hours: [{ hour: "2026-09-01T00", samples: 12, running_secs: 1800 }],
        },
      ],
    });
    expect(cellValue(g.get("2026-09-01|9"), "running")).toBeCloseTo(0.5, 5);
  });

  // Drawing an unreachable Agent as "zero sessions" puts a cold cell on a busy hour.
  it("does not render an unknown session count as zero sessions", () => {
    const c = grid.get("2026-09-01|10")!;
    expect(c.runningSecs).toBe(3600);
    expect(isUnmeasured(c, "sessions")).toBe(true);
    // The running metric has no "unknown": the running seconds are known.
    expect(isUnmeasured(c, "running")).toBe(false);
  });

  it("recomputes the maximum per metric", () => {
    const days = ["2026-09-01"];
    expect(maxValue(grid, days, "sessions")).toBeCloseTo(9000 / 5400, 5);
    expect(maxValue(grid, days, "busy")).toBeCloseTo(1800 / 5400, 5);
  });

  it("survives a null or empty response", () => {
    expect(buildGrid(null).size).toBe(0);
    expect(buildGrid({ ...RES, observed: [], members: [] }).size).toBe(0);
  });
});

describe("colour levels", () => {
  // For the count metrics "up but zero sessions" is the lowest level. It differs from grey
  // (stopped), so it cannot be given level 0 and left undrawn.
  it("puts zero on the lowest level for the count metrics", () => {
    expect(cellLevel(0, 4, true)).toBe(1);
    expect(cellLevel(0.1, 4, true)).toBe(2);
    expect(cellLevel(4, 4, true)).toBe(5);
  });

  it("uses all five levels for the running metric", () => {
    expect(cellLevel(0.01, 1, false)).toBe(1);
    expect(cellLevel(1, 1, false)).toBe(5);
  });

  it("falls back to level 1 when the maximum is 0, so no NaN class is built", () => {
    expect(cellLevel(0, 0, true)).toBe(1);
    expect(cellLevel(0, 0, false)).toBe(1);
  });

  it("splits the legend bands the same way as the levels", () => {
    expect(levelBand(1, 4, true)).toEqual([0, 0]);
    expect(levelBand(2, 4, true)).toEqual([0, 1]);
    expect(levelBand(5, 4, true)).toEqual([3, 4]);
    expect(levelBand(1, 1, false)).toEqual([0, 0.2]);
  });
});

describe("minute display", () => {
  // Showing 30 seconds as "0 minutes" reads as "not running".
  it("shows any non-zero uptime as at least one minute", () => {
    expect(minutesOf(0)).toBe(0);
    expect(minutesOf(20)).toBe(1);
    expect(minutesOf(3600)).toBe(60);
  });
});
