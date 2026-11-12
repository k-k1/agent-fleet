// The reclaimable figure of the low-disk notice: only from a recent finished walk, never
// started here, and gone once a cleanup has used that walk up.
import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.fn();
vi.mock("../api/client.ts", () => ({ api: (...a: unknown[]) => api(...a), apiJSON: async () => ({}), getTenant: () => "t" }));

import { CLOCK_SKEW_MS, RECENT_WALK_MS, __test, loadCachedWalk, markCleaned, publishWalk, reclaimBytes, reclaimEstimate } from "./homeReclaim.ts";

const GIB = 1024 ** 3;
const walk = (ageMs: number, over: Record<string, unknown> = {}) => ({
  measured_at: new Date(Date.now() - ageMs).toISOString(),
  categories: [
    {
      key: "tool_cache",
      parts: [
        { name: "go-build", bytes: 2 * GIB, reclaimable: true },
        { name: "gradle-caches", bytes: 9 * GIB, reclaimable: true },
        { name: "other", bytes: 7 * GIB },
      ],
    },
  ],
  repos: [
    { bytes: 5 * GIB, regenerable: GIB },
    { bytes: 5 * GIB, regenerable: 4 * GIB, unjudged: true },
  ],
  ...over,
});

beforeEach(() => {
  __test.reset();
  api.mockReset();
});

describe("reclaimBytes", () => {
  it("counts judged build output and reclaimable tool caches, not Gradle's whole rows or unjudged repos", () => {
    expect(reclaimBytes(walk(0))).toBe(3 * GIB);
  });
});

describe("reclaimEstimate", () => {
  it("is null without a walk", () => {
    expect(reclaimEstimate()).toBeNull();
  });
  it("shows a recent walk and drops it once it is older than RECENT_WALK_MS", () => {
    publishWalk(walk(RECENT_WALK_MS - 60_000));
    expect(reclaimEstimate()).toBe(3 * GIB);
    expect(reclaimEstimate(Date.now() + 2 * 60_000)).toBeNull();
  });
  it("never shows a walk that is already too old", () => {
    publishWalk(walk(RECENT_WALK_MS + 60_000));
    expect(reclaimEstimate()).toBeNull();
  });
  it("rejects a walk stamped in the future beyond the clock-skew allowance", () => {
    publishWalk(walk(-24 * 3600 * 1000));
    expect(reclaimEstimate()).toBeNull();
    __test.reset();
    publishWalk(walk(-(CLOCK_SKEW_MS - 60_000)));
    expect(reclaimEstimate()).toBe(3 * GIB);
  });
  it("shows nothing for a walk with nothing reclaimable", () => {
    publishWalk(walk(0, { categories: [], repos: [] }));
    expect(reclaimEstimate()).toBeNull();
  });
  it("is gone after a cleanup, and the same walk cannot bring it back", () => {
    const w = walk(1000);
    publishWalk(w);
    markCleaned();
    expect(reclaimEstimate()).toBeNull();
    publishWalk(w); // the Agent still holds it until a new walk finishes
    expect(reclaimEstimate()).toBeNull();
    publishWalk(walk(0));
    expect(reclaimEstimate()).toBe(3 * GIB);
  });
});

describe("loadCachedWalk", () => {
  it("reads the Agent's last result with one GET and never asks for a walk", async () => {
    api.mockResolvedValue({ running: false, scanned: 0, result: walk(1000) });
    await loadCachedWalk();
    expect(api).toHaveBeenCalledTimes(1);
    expect(api.mock.calls[0]).toEqual(["api/cleanup/home"]);
    expect(reclaimEstimate()).toBe(3 * GIB);
  });
  it("leaves the notice without a figure when there is no result or the call fails", async () => {
    api.mockResolvedValueOnce({ running: false, scanned: 0, result: null });
    await loadCachedWalk();
    api.mockRejectedValueOnce(new Error("x"));
    await loadCachedWalk();
    expect(reclaimEstimate()).toBeNull();
  });
});
