import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const memoQueueList = vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "r", category: "x" }] }));
const memoQueueDeliveries = vi.fn(async (_id: string, _open?: boolean): Promise<unknown> => [{ id: "d0", session: "run-old", state: "closed", closedAt: "2026-11-01T00:45:00Z" }]);
vi.mock("../memo/api.ts", () => ({ memoQueueList: () => memoQueueList(), memoQueueDeliveries: (id: string, o?: boolean) => memoQueueDeliveries(id, o) }));

const { acquireAutoRuns, reloadAutoRuns, useAutoRunStore } = await import("./store.ts");

beforeEach(() => {
  memoQueueList.mockClear();
  memoQueueDeliveries.mockClear();
});
afterEach(() => useAutoRunStore.setState({ queues: [], deliveries: new Map(), loaded: false, scope: "" }));

const settle = () => new Promise((r) => setTimeout(r, 0));

describe("the shared auto-run feed", () => {
  it("polls once for two subscribers and stops after the last one leaves", async () => {
    const a = acquireAutoRuns("s");
    const b = acquireAutoRuns("s");
    await settle();
    expect(memoQueueList).toHaveBeenCalledTimes(1);
    a();
    b();
    expect(useAutoRunStore.getState().deliveries.get("run-old")?.state).toBe("closed");
  });

  it("keeps what a scope knew across a pause of the poll, and drops it for another scope", async () => {
    const first = acquireAutoRuns("s");
    await settle();
    first();
    // The history no longer holds the closed run; a later poll of the same scope must not forget it.
    memoQueueDeliveries.mockImplementation(async () => []);
    const back = acquireAutoRuns("s");
    await settle();
    expect(useAutoRunStore.getState().deliveries.get("run-old")?.state).toBe("closed");
    back();
    acquireAutoRuns("other")();
    expect(useAutoRunStore.getState().deliveries.size).toBe(0);
  });
});

describe("the shared auto-run feed over time", () => {
  afterEach(() => vi.useRealTimers());

  it("goes on polling after one of two subscribers leaves, and stops after the last", async () => {
    vi.useFakeTimers();
    const a = acquireAutoRuns("s");
    const b = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    expect(memoQueueList).toHaveBeenCalledTimes(1);
    a();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(memoQueueList).toHaveBeenCalledTimes(2);
    b();
    await vi.advanceTimersByTimeAsync(90_000);
    expect(memoQueueList).toHaveBeenCalledTimes(2);
  });

  it("reads again after a reload that came while a list was in flight, and drops the stale category answer", async () => {
    vi.useFakeTimers();
    let first: (v: unknown) => void = () => {};
    memoQueueList.mockImplementationOnce(() => new Promise((r) => (first = r)) as never);
    const off = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    reloadAutoRuns(); // a saved setting: the list in flight may predate it
    first({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "r", category: "x" }] });
    await vi.advanceTimersByTimeAsync(0);
    expect(memoQueueList).toHaveBeenCalledTimes(2);
    off();
  });

  it("drops an answer that arrives after the scope changed", async () => {
    vi.useFakeTimers();
    let late: (v: unknown) => void = () => {};
    memoQueueList.mockImplementationOnce(() => new Promise((r) => (late = r)) as never);
    const old = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    old();
    const next = acquireAutoRuns("other");
    late({ tenantEnabled: true, queues: [{ categoryId: "stale", repo: "r", category: "x" }] });
    await vi.advanceTimersByTimeAsync(0);
    expect(useAutoRunStore.getState().queues.some((q) => q.categoryId === "stale")).toBe(false);
    next();
  });
});
