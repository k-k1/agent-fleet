// The feed's whole reason to exist is that a tick with no frame is still a data point — and
// the exact condition under which that is TRUE rather than invented. Both directions are
// pinned here, because getting it wrong is invisible: the chart just draws a confident line.
import { describe, it, expect, beforeEach, vi } from "vitest";

const healthy = vi.fn(() => true);
vi.mock("../push/events.ts", () => ({
  onPush: () => () => {},
  pushHealthy: () => healthy(),
}));
vi.mock("../api/client.ts", () => ({
  api: () => Promise.resolve({}),
  getTenant: () => tenant,
}));

let tenant = "acme";
const { __test } = await import("./wsStatsFeed.ts");

const RUNNING = { running: true, mem_used: 1073741824, mem_max: 4294967296, cpu_pct: 12, disk_used: 5, disk_total: 50 };

beforeEach(() => {
  tenant = "acme";
  healthy.mockReturnValue(true);
  __test.reset();
});

describe("wsStatsFeed", () => {
  // The CP does not send an unchanged stats frame, so an idle workspace delivers nothing for
  // minutes. Holding the last value is what turns that into a moving chart.
  it("appends a point per tick even when no new frame arrived", () => {
    __test.setLatest(RUNNING);
    __test.sample();
    __test.sample();
    __test.sample();
    const s = __test.samples();
    expect(s.length).toBe(3);
    expect(s.map((x) => x.memUsed)).toEqual([1073741824, 1073741824, 1073741824]);
    // Timestamps are what the chart maps x by, so they must be real.
    expect(s[2].t).toBeGreaterThanOrEqual(s[0].t);
  });

  // "Unchanged" is a claim the push stream makes. With the stream down nobody is making it,
  // and continuing the line would be the chart inventing data.
  it("records a gap instead of holding the value when the stream is down", () => {
    __test.setLatest(RUNNING, Date.now() - 60_000); // last reading is a minute old
    healthy.mockReturnValue(false);
    __test.sample();
    const [s] = __test.samples();
    expect(s.memUsed).toBeNull();
    expect(s.cpu).toBeNull();
  });

  // A poll that just succeeded is believable for a couple of intervals even without the
  // stream — otherwise the fallback path would draw nothing but holes.
  it("trusts a fresh reading briefly while polling", () => {
    healthy.mockReturnValue(false);
    __test.setLatest(RUNNING, Date.now());
    __test.sample();
    expect(__test.samples()[0].memUsed).toBe(1073741824);
  });

  it("keeps a stopped workspace out of the series rather than plotting zeros", () => {
    __test.setLatest({ running: false });
    __test.sample();
    const [s] = __test.samples();
    expect(s.memUsed).toBeNull();
    expect(s.cpu).toBeNull();
  });

  // A tenant switch is a different workspace; continuing the series would attribute one
  // workspace's load to another.
  it("drops the series when the tenant changes", () => {
    __test.setLatest(RUNNING);
    __test.sample();
    expect(__test.samples().length).toBe(1);
    tenant = "other";
    __test.sample();
    expect(__test.samples().length).toBe(1); // the pre-switch point is gone, not appended to
  });

  it("carries an OOM kill as part of the sample so it survives the flag", () => {
    __test.setLatest({ ...RUNNING, oom_recent: true });
    __test.sample();
    __test.setLatest({ ...RUNNING });
    __test.sample();
    expect(__test.samples().map((s) => s.oom)).toEqual([true, false]);
  });
});
