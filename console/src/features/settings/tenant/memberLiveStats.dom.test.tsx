// useLiveStats turns the live-stats reply into the poller's success / failure: an error reply
// is a failure (the last reading stays on screen, the next try backs off), while a null body is
// a success with nothing to show (the reading clears, the interval stays).
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(() => Promise.resolve({})),
}));

import { useLiveStats, LIVE_INTERVAL_MS, LIVE_DWELL_MS, type LiveReading } from "./memberLiveStats.ts";

const RUNNING = { running: true, mem_used: 5, mem_max: 10, cpu_pct: 1 };
let seen: Record<string, LiveReading> = {};
function Probe() {
  seen = useLiveStats("t", ["k"]);
  return null;
}

let root: Root;
beforeEach(() => {
  vi.useFakeTimers();
  api.mockReset();
  seen = {};
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root.unmount());
  vi.useRealTimers();
});

const tick = (ms: number) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

describe("useLiveStats reply handling", () => {
  it("keeps the last reading on an error reply and backs off to 2x the interval", async () => {
    api.mockResolvedValueOnce(RUNNING).mockResolvedValue({ error: { message: "boom" } });
    act(() => root.render(<Probe />));
    await tick(LIVE_DWELL_MS + 1);
    expect(api).toHaveBeenCalledTimes(1);
    expect(seen.k?.mem_used).toBe(5);
    await tick(LIVE_INTERVAL_MS);
    expect(api).toHaveBeenCalledTimes(2);
    expect(seen.k?.mem_used).toBe(5); // the error did not clear it
    await tick(LIVE_INTERVAL_MS); // only 1x since the failure: still backing off
    expect(api).toHaveBeenCalledTimes(2);
    await tick(LIVE_INTERVAL_MS + 1);
    expect(api).toHaveBeenCalledTimes(3);
  });

  it("clears the reading on a null body and keeps the normal interval", async () => {
    api.mockResolvedValueOnce(RUNNING).mockResolvedValue(null);
    act(() => root.render(<Probe />));
    await tick(LIVE_DWELL_MS + 1);
    expect(seen.k?.mem_used).toBe(5);
    await tick(LIVE_INTERVAL_MS);
    expect(api).toHaveBeenCalledTimes(2);
    expect(seen.k).toBeUndefined();
    await tick(LIVE_INTERVAL_MS + 1);
    expect(api).toHaveBeenCalledTimes(3);
  });
});
