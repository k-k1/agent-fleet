import { afterEach, describe, expect, it, vi } from "vitest";

const memoQueueList = vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "r", category: "x" }] }));
const memoQueueDeliveries = vi.fn(async (_id: string, _open?: boolean): Promise<unknown> => [{ id: "d0", session: "run-old", state: "closed", closedAt: "2026-11-01T00:45:00Z" }]);
vi.mock("../memo/api.ts", () => ({ memoQueueList: () => memoQueueList(), memoQueueDeliveries: (id: string, o?: boolean) => memoQueueDeliveries(id, o) }));

const { acquireAutoRuns, useAutoRunStore } = await import("./store.ts");

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
