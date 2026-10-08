import { describe, it, expect } from "vitest";
import { reachTurn, REACH_MARGIN, REACH_MAX_LINES, REACH_MAX_PAGES } from "./reachTurn.ts";

// A fake history. `stride` is how far apart the turns' idx are per cursor position: 1 for claude
// (idx = line number = cursor unit), more for a store-backed agent whose turns keep the source
// event's idx while the cursor counts array positions.
function history(start: number, opts: { fail?: boolean; stuck?: boolean; cancelAfter?: number; stride?: number } = {}) {
  const stride = opts.stride ?? 1;
  const st = { first: start, limits: [] as number[], calls: 0 };
  const deps = {
    oldestIdx: () => st.first * stride,
    exhausted: () => st.first <= 0,
    cursor: () => st.first,
    page: async (limit: number) => {
      st.calls++;
      st.limits.push(limit);
      if (opts.fail) return false;
      if (!opts.stuck) st.first = Math.max(0, st.first - limit);
      return true;
    },
    cancelled: () => opts.cancelAfter !== undefined && st.calls >= opts.cancelAfter,
  };
  return { st, deps };
}

describe("reachTurn", () => {
  it("fetches nothing when the turn is already held", async () => {
    const h = history(1000);
    expect(await reachTurn(1200, h.deps)).toBe("mounted");
    expect(h.st.calls).toBe(0);
  });

  it("pages back in one request sized to the gap, with a margin", async () => {
    const h = history(2000);
    expect(await reachTurn(1000, h.deps)).toBe("reached");
    expect(h.st.limits).toEqual([2000 - (1000 - REACH_MARGIN)]);
    expect(h.st.first).toBeLessThanOrEqual(1000 - REACH_MARGIN);
  });

  it("splits a gap larger than the server window into several requests", async () => {
    const h = history(7000);
    expect(await reachTurn(100, h.deps)).toBe("reached");
    expect(h.st.calls).toBe(2);
    expect(h.st.limits.every((l) => l <= 4000)).toBe(true);
  });

  it("judges reach on the oldest idx held, not the cursor, when idx is sparse", async () => {
    // 600 cursor positions held, every turn 10 idx apart: the oldest idx held is 6000. A hit at idx
    // 1000 is NOT mounted although 1000 >= the cursor (600) — the old test said it was.
    const h = history(600, { stride: 10 });
    expect(await reachTurn(1000, h.deps)).toBe("reached");
    expect(h.st.calls).toBeGreaterThan(0);
    expect(h.st.first * 10).toBeLessThanOrEqual(1000 - REACH_MARGIN);
  });

  it("stops at the start of the transcript", async () => {
    const h = history(300);
    expect(await reachTurn(10, h.deps)).toBe("reached");
    expect(h.st.first).toBe(0);
  });

  it("refuses a gap past the cap without any request", async () => {
    const h = history(REACH_MAX_LINES + 5000);
    expect(await reachTurn(1000, h.deps)).toBe("too-far");
    expect(h.st.calls).toBe(0);
  });

  it("gives up when a page fails", async () => {
    const h = history(3000, { fail: true });
    expect(await reachTurn(100, h.deps)).toBe("failed");
    expect(h.st.calls).toBe(1);
  });

  it("never loops on a cursor that does not move", async () => {
    const h = history(3000, { stuck: true });
    expect(await reachTurn(100, h.deps)).toBe("failed");
    expect(h.st.calls).toBe(1);
  });

  it("never makes more requests than the page cap", async () => {
    // Each page only moves 50 back (a server that trims): the cap, not the gap, ends it.
    let first = 400;
    let calls = 0;
    const out = await reachTurn(0, {
      oldestIdx: () => first,
      exhausted: () => first <= 0,
      cursor: () => first,
      page: async () => { calls++; first -= 50; return true; },
      cancelled: () => false,
    });
    expect(out).toBe("too-far");
    expect(calls).toBe(REACH_MAX_PAGES);
  });

  it("spends one budget across pages when the server trims them", async () => {
    // Each page moves the cursor by the limit but the oldest idx only by 1000 (1 MiB trim): the
    // idx gap stays open while the cursor walks. The cumulative cap, not 4 x 4000, ends it.
    let cursor = 10000;
    let oldest = 10000;
    const limits: number[] = [];
    const out = await reachTurn(3000, {
      oldestIdx: () => oldest,
      exhausted: () => cursor <= 0,
      cursor: () => cursor,
      page: async (limit) => { limits.push(limit); cursor = Math.max(0, cursor - limit); oldest -= 1000; return true; },
      cancelled: () => false,
    });
    expect(out).toBe("too-far");
    expect(limits.reduce((a, b) => a + b, 0)).toBeLessThanOrEqual(REACH_MAX_LINES);
  });

  it("stops between pages once cancelled", async () => {
    const h = history(7000, { cancelAfter: 1 });
    expect(await reachTurn(100, h.deps)).toBe("cancelled");
    expect(h.st.calls).toBe(1);
  });
});
