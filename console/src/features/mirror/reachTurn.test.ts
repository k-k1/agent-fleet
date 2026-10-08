import { describe, it, expect } from "vitest";
import { reachTurn, REACH_MARGIN, REACH_MAX_LINES, REACH_MAX_PAGES } from "./reachTurn.ts";

// A fake history: the page call moves firstLine back by the limit, floored at 0.
function history(start: number, opts: { fail?: boolean; stuck?: boolean; cancelAfter?: number } = {}) {
  const st = { first: start, limits: [] as number[], calls: 0 };
  const deps = {
    firstLine: () => st.first,
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
    let first = 7000;
    let calls = 0;
    const out = await reachTurn(0, {
      firstLine: () => first,
      page: async () => { calls++; first -= 50; return true; },
      cancelled: () => false,
    });
    expect(out).toBe("failed");
    expect(calls).toBe(REACH_MAX_PAGES);
  });

  it("stops between pages once cancelled", async () => {
    const h = history(7000, { cancelAfter: 1 });
    expect(await reachTurn(100, h.deps)).toBe("cancelled");
    expect(h.st.calls).toBe(1);
  });
});
