import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const memoQueueList = vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "r", category: "x" }] }));
const memoQueueDeliveries = vi.fn(async (_id: string, _open?: boolean, _since?: string): Promise<unknown> => [{ id: "d0", session: "run-old", state: "closed", closedAt: "2026-11-01T00:45:00Z" }]);
vi.mock("../memo/api.ts", () => ({ memoQueueList: () => memoQueueList(), memoQueueDeliveries: (id: string, o?: boolean, since?: string) => memoQueueDeliveries(id, o, since) }));

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

describe("a run that closes after it left the newest fifty", () => {
  afterEach(() => vi.useRealTimers());

  it("asks for the closings since the latest one known, and takes the end the answer carries", async () => {
    vi.useFakeTimers();
    memoQueueDeliveries.mockImplementation(async () => [
      { id: "d1", session: "run-a", state: "closed", closedAt: "2026-11-01T00:45:00Z" },
      { id: "d2", session: "run-b", state: "open", createdAt: "2026-11-01T00:50:00Z" },
    ]);
    const off = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    expect(memoQueueDeliveries.mock.calls[0]).toEqual(["c1", true, ""]);
    // d2 is no longer in the newest fifty; only the closed-since part brings it back, closed.
    memoQueueDeliveries.mockImplementation(async () => [{ id: "d2", session: "run-b", state: "closed", outcome: "clean", closedAt: "2026-11-01T01:00:00Z" }]);
    await vi.advanceTimersByTimeAsync(30_000);
    // The bound is d1's closing: d2 was created after it, so no earlier stamp is needed.
    expect(memoQueueDeliveries.mock.calls[1]).toEqual(["c1", true, "2026-11-01T00:45:00Z"]);
    expect(useAutoRunStore.getState().deliveries.get("run-b")).toMatchObject({ state: "closed", closedAt: "2026-11-01T01:00:00Z" });
    off();
  });

  it("starts at the creation of a run known open when nothing has closed yet, or when it is older than the latest closing", async () => {
    vi.useFakeTimers();
    memoQueueDeliveries.mockImplementation(async () => [{ id: "d2", session: "run-b", state: "open", createdAt: "2026-11-01T00:10:00Z" }]);
    const off = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    // The server keeps answering the open run; newer runs close around it.
    memoQueueDeliveries.mockImplementation(async () => [
      { id: "d9", session: "run-z", state: "closed", closedAt: "2026-11-01T05:00:00Z" },
      { id: "d2", session: "run-b", state: "open", createdAt: "2026-11-01T00:10:00Z" },
    ]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(memoQueueDeliveries.mock.calls[1]).toEqual(["c1", true, "2026-11-01T00:10:00Z"]);
    // Newer runs closing does not move the bound past the run still known open.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(memoQueueDeliveries.mock.calls[2]).toEqual(["c1", true, "2026-11-01T00:10:00Z"]);
    off();
  });

  it("keeps the bound at a run an older Control Plane's answer left out, until its closing is read", async () => {
    vi.useFakeTimers();
    memoQueueDeliveries.mockImplementation(async () => [{ id: "d2", session: "run-b", state: "open", createdAt: "2026-11-01T00:10:00Z" }]);
    const off = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    // An older Control Plane ignores closed_since: d2 has closed and is not in its newest fifty.
    memoQueueDeliveries.mockImplementation(async () => [{ id: "d9", session: "run-z", state: "closed", closedAt: "2026-11-01T05:00:00Z" }]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(useAutoRunStore.getState().deliveries.get("run-b")?.state).toBe("");
    // A current one answers the next read: the bound is still d2's creation, not d9's closing.
    memoQueueDeliveries.mockImplementation(async () => [{ id: "d2", session: "run-b", state: "closed", closedAt: "2026-11-01T01:00:00Z" }]);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(memoQueueDeliveries.mock.calls[2]).toEqual(["c1", true, "2026-11-01T00:10:00Z"]);
    expect(useAutoRunStore.getState().deliveries.get("run-b")?.state).toBe("closed");
    off();
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

  it("reads on its own after a reload, never publishes the list that predates it, and does not wait for one that hangs", async () => {
    vi.useFakeTimers();
    let stale: (v: unknown) => void = () => {};
    memoQueueList.mockImplementationOnce(() => new Promise((r) => (stale = r)) as never);
    const off = acquireAutoRuns("s");
    await vi.advanceTimersByTimeAsync(0);
    // The first list never answers; the reload (a saved setting) starts its own read at once.
    memoQueueList.mockImplementationOnce(async () => ({ tenantEnabled: false, queues: [{ categoryId: "new", repo: "r", category: "n" }] }));
    reloadAutoRuns();
    await vi.advanceTimersByTimeAsync(0);
    expect(memoQueueList).toHaveBeenCalledTimes(2);
    expect(useAutoRunStore.getState().queues.map((q) => q.categoryId)).toEqual(["new"]);
    // The old list arrives late with the old setting: it must not replace the new one.
    stale({ tenantEnabled: true, queues: [{ categoryId: "old", repo: "r", category: "o" }] });
    await vi.advanceTimersByTimeAsync(0);
    expect(useAutoRunStore.getState().queues.map((q) => q.categoryId)).toEqual(["new"]);
    expect(useAutoRunStore.getState().tenantEnabled).toBe(false);
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
