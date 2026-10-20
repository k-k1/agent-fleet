import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
vi.mock("../../../core/api/client.ts", () => ({ api: vi.fn() }));

import { LivePoller, parseReading } from "./memberLiveStats.ts";
import type { LiveReading } from "./memberLiveStats.ts";

const R: LiveReading = { mem_used: 1, mem_max: 2, cpu_pct: 3 };

function deferred() {
  let resolve!: (r: LiveReading | null) => void;
  let reject!: (e: unknown) => void;
  const p = new Promise<LiveReading | null>((a, b) => ((resolve = a), (reject = b)));
  return { p, resolve, reject };
}
const flush = () => vi.advanceTimersByTimeAsync(0);

describe("LivePoller", () => {
  beforeEach(() => vi.useFakeTimers());
  afterEach(() => vi.useRealTimers());

  it("fetches a wanted member once, then again after the interval", async () => {
    const load = vi.fn(async () => R);
    const got = vi.fn();
    const p = new LivePoller(load, got, false, { interval: 15000, max: 3 });
    p.setWanted(["a"]);
    await flush();
    expect(load).toHaveBeenCalledTimes(1);
    expect(got).toHaveBeenCalledWith("a", R);
    await vi.advanceTimersByTimeAsync(14000);
    expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1100);
    expect(load).toHaveBeenCalledTimes(2);
    p.stop();
  });

  it("never overlaps requests for one member and caps concurrency", async () => {
    const pending: ReturnType<typeof deferred>[] = [];
    const load = vi.fn(() => {
      const d = deferred();
      pending.push(d);
      return d.p;
    });
    const p = new LivePoller(load, vi.fn(), false, { interval: 1000, max: 3 });
    p.setWanted(["a", "b", "c", "d", "e"]);
    await flush();
    expect(load).toHaveBeenCalledTimes(3);
    // Far past the interval, still the same three in flight: no overlap, no extra starts.
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(3);
    pending[0].resolve(R);
    await flush();
    expect(load).toHaveBeenCalledTimes(4);
    p.stop();
  });

  it("does not fetch when scrolled back within the interval, and drops unwanted members", async () => {
    const load = vi.fn(async () => R);
    const p = new LivePoller(load, vi.fn(), false, { interval: 15000, max: 3 });
    p.setWanted(["a"]);
    await flush();
    p.setWanted([]);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(load).toHaveBeenCalledTimes(1);
    p.setWanted(["a"]); // due again by now
    await flush();
    expect(load).toHaveBeenCalledTimes(2);
    p.setWanted([]);
    p.setWanted(["a"]); // jitter at the viewport edge
    await flush();
    expect(load).toHaveBeenCalledTimes(2);
    p.stop();
  });

  it("pauses while hidden and resumes with one catch-up fetch", async () => {
    const load = vi.fn(async () => R);
    const p = new LivePoller(load, vi.fn(), true, { interval: 1000, max: 3 });
    p.setWanted(["a"]);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).not.toHaveBeenCalled();
    p.setPaused(false);
    await flush();
    expect(load).toHaveBeenCalledTimes(1);
    p.setPaused(true);
    await vi.advanceTimersByTimeAsync(10_000);
    expect(load).toHaveBeenCalledTimes(1);
    p.stop();
  });

  it("backs off after failures instead of retrying", async () => {
    const load = vi.fn(async () => {
      throw new Error("x");
    });
    const got = vi.fn();
    const p = new LivePoller(load, got, false, { interval: 1000, max: 3 });
    p.setWanted(["a"]);
    await flush();
    expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1500); // 2× interval after the first failure
    expect(load).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(600);
    expect(load).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(3500); // 4×
    expect(load).toHaveBeenCalledTimes(2);
    expect(got).not.toHaveBeenCalled();
    p.stop();
  });

  it("ignores a response that lands after stop()", async () => {
    const d = deferred();
    const got = vi.fn();
    const load = vi.fn(() => d.p);
    const p = new LivePoller(load, got, false);
    p.setWanted(["a"]);
    await flush();
    p.stop();
    d.resolve(R);
    await vi.advanceTimersByTimeAsync(60_000);
    expect(got).not.toHaveBeenCalled();
    expect(load).toHaveBeenCalledTimes(1);
  });
});

describe("parseReading", () => {
  it("keeps only a running workspace with a memory figure", () => {
    expect(parseReading({ running: true, mem_used: 5, mem_max: 10, cpu_pct: 0 })).toEqual({ mem_used: 5, mem_max: 10, cpu_pct: 0 });
    expect(parseReading({ running: true, mem_used: 5, mem_max: 0 })?.mem_max).toBeUndefined();
    expect(parseReading({ running: false })).toBeNull();
    expect(parseReading({ running: true })).toBeNull();
    expect(parseReading({ error: { code: "x" } })).toBeNull();
  });
});
