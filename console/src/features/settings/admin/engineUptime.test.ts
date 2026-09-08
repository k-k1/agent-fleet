// The pure layer of the engine heatmap (ADR 0071).
//
// One property dominates: an hour the control plane did not watch must stay distinguishable
// from an hour the engine was asleep. Collapsing the two turns a CP outage into a confident
// "the GPU was idle all weekend", which is a claim somebody acts on.
import { describe, it, expect } from "vitest";
import {
  buildEngineGrid,
  engineCellState,
  engineCellValue,
  engineMetricSecs,
  secsUntil,
  splitHM,
  windowIsPartial,
} from "./engineUptime.ts";

// Fixed at UTC+09:00 in vitest.config, so a UTC hour maps to local hour + 9. Read this from the
// runtime rather than assuming: a hard-coded 9 turns a config change into a puzzle.
const OFFSET_H = -new Date("2026-09-08T00:00:00Z").getTimezoneOffset() / 60;

const res = (hours: Parameters<typeof buildEngineGrid>[0] extends null ? never : object[]) =>
  ({ engine: "image", from: "2026-09-08", to: "2026-09-08", interval_secs: 30, hours }) as never;

describe("buildEngineGrid", () => {
  it("keeps unobserved, stopped and running as three different things", () => {
    const grid = buildEngineGrid(
      res([
        // Watched and running for half the hour.
        { hour: "2026-09-08T04", samples: 120, observed_secs: 3600, running_secs: 1800 },
        // Watched and idle: a row with no time in any state.
        { hour: "2026-09-08T05", samples: 120, observed_secs: 3600 },
        // 06 is absent: the CP was not running.
      ]),
    );
    const at = (utcHour: number) => grid.get("2026-09-08|" + ((utcHour + OFFSET_H) % 24));

    expect(engineCellState(at(4), "running")).toBe("running");
    expect(engineCellState(at(5), "running")).toBe("stopped");
    // The decisive one. `undefined` — no cell at all — must not read as stopped.
    expect(engineCellState(at(6), "running")).toBe("unobserved");
  });

  it("divides by what was observed, not by an hour", () => {
    // The hour still in progress: the controller has only watched 600 of its seconds, and it was
    // running for all of them. Dividing by 3600 would draw a busy engine as 17% and make the
    // colour show missing observation rather than uptime.
    const grid = buildEngineGrid(
      res([{ hour: "2026-09-08T04", samples: 20, observed_secs: 600, running_secs: 600 }]),
    );
    const c = grid.get("2026-09-08|" + ((4 + OFFSET_H) % 24));
    expect(engineCellValue(c, "running")).toBe(1);
  });

  it("caps a cell at 100%", () => {
    // A sample recorded a fraction past an hour boundary. "104% up" destroys confidence in a
    // panel whose entire job is to be believed.
    const grid = buildEngineGrid(
      res([{ hour: "2026-09-08T04", samples: 2, observed_secs: 60, running_secs: 62 }]),
    );
    expect(engineCellValue(grid.get("2026-09-08|" + ((4 + OFFSET_H) % 24)), "running")).toBe(1);
  });

  it("separates the time that answered from the time that only billed", () => {
    // The cold start (165-197 s measured) and the drain (427-477 s measured) both cost money
    // and neither serves a request. A panel that showed only one of the two numbers would
    // either overstate the service or hide the bill.
    const grid = buildEngineGrid(
      res([
        {
          hour: "2026-09-08T04",
          samples: 120,
          observed_secs: 3600,
          running_secs: 1200,
          starting_secs: 180,
          draining_secs: 420,
        },
      ]),
    );
    const c = grid.get("2026-09-08|" + ((4 + OFFSET_H) % 24));
    expect(engineMetricSecs(c, "running")).toBe(1200);
    expect(engineMetricSecs(c, "up")).toBe(1800);
    // An hour that only drained is "a box existed" but never "able to answer": the two metrics
    // must disagree about its state, not merely about its number.
    const drained = buildEngineGrid(
      res([{ hour: "2026-09-08T05", samples: 120, observed_secs: 3600, draining_secs: 600 }]),
    );
    const d = drained.get("2026-09-08|" + ((5 + OFFSET_H) % 24));
    expect(engineCellState(d, "running")).toBe("stopped");
    expect(engineCellState(d, "up")).toBe("running");
  });
});

describe("windowIsPartial", () => {
  // 🔴 The guard against the panel's one available lie: the rolling count is in the CP's memory
  // only, so a control plane replaced two minutes ago reports 0 requests for an engine somebody
  // is talking to right now.
  it("is true only while the process has counted less than its own window", () => {
    expect(windowIsPartial({ window_secs: 300, window_counted_secs: 90 })).toBe(true);
    expect(windowIsPartial({ window_secs: 300, window_counted_secs: 300 })).toBe(false);
    // A second of slack: the counter starts a hair after the window is read, and a warning that
    // is permanently on is a warning the reader learns to skip.
    expect(windowIsPartial({ window_secs: 300, window_counted_secs: 299.5 })).toBe(false);
    // An older CP that sends no counted figure at all: say nothing rather than accuse it.
    expect(windowIsPartial({ window_secs: 300 })).toBe(false);
  });
});

describe("secsUntil", () => {
  it("returns null rather than 0 for an answer that is not there", () => {
    const now = Date.parse("2026-09-08T04:00:00Z");
    expect(secsUntil("2026-09-08T04:10:00Z", now)).toBe(600);
    expect(secsUntil("2026-09-08T03:50:00Z", now)).toBe(-600);
    // A countdown showing 0 is a claim that it is happening right now; absent is absent.
    expect(secsUntil(undefined, now)).toBeNull();
    expect(secsUntil("not a date", now)).toBeNull();
  });
});

describe("splitHM", () => {
  it("never rounds a short run down to zero minutes", () => {
    // "0 分" for a 40-second start reads as "it never came up", which is the opposite of what
    // happened.
    expect(splitHM(40)).toEqual({ hours: 0, mins: 1 });
    expect(splitHM(3720)).toEqual({ hours: 1, mins: 2 });
    expect(splitHM(0)).toEqual({ hours: 0, mins: 0 });
  });
});
