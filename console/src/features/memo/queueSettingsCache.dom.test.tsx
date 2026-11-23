import { describe, expect, it } from "vitest";
import { queueScope, useQueueSettingsCache } from "./queueSettingsCache.ts";
import { readQueue } from "./queues.ts";

describe("a saved queue and its fireBlock", () => {
  const row = (fireBlock: unknown, maxActive = 1) => readQueue({ categoryId: "c1", maxActive, waiting: undefined, fireBlock })!;
  const scope = () => queueScope();

  it("drops a slots-full reason when a save may have raised the limit, and keeps the others", () => {
    const c = useQueueSettingsCache.getState();
    c.reset();
    c.publish(scope(), c.begin(), { tenantEnabled: true, repoSettings: false, queues: [row({ reason: "no_place", detail: { by: "category" } })] });
    useQueueSettingsCache.getState().applySaved(scope(), row(undefined, 2));
    expect(useQueueSettingsCache.getState().queues[0].fireBlock).toBeUndefined();

    c.reset();
    c.publish(scope(), c.begin(), { tenantEnabled: true, repoSettings: false, queues: [row({ reason: "prerequisites", detail: { blocked: 2 } })] });
    useQueueSettingsCache.getState().applySaved(scope(), row(undefined, 2));
    expect(useQueueSettingsCache.getState().queues[0].fireBlock?.reason).toBe("prerequisites");
  });
});

describe("a write whose outcome is unknown", () => {
  const row = (spendCapUsd: number) => readQueue({ categoryId: "c1", spendCapUsd })!;
  const wire = (spendCapUsd: number) => ({ tenantEnabled: true, repoSettings: true, queues: [row(spendCapUsd)] });

  it("drops the reads begun before it and stays unsure until a later read lands", () => {
    const c = useQueueSettingsCache.getState();
    c.reset();
    c.publish(queueScope(), c.begin(), wire(60));
    const early = c.begin(); // a read already on its way, with the row from before the write
    c.markUnsure(queueScope(), "c1");
    expect(useQueueSettingsCache.getState().unsure).toEqual(["c1"]);
    c.publish(queueScope(), early, wire(60));
    expect(useQueueSettingsCache.getState().unsure).toEqual(["c1"]);
    c.publish(queueScope(), c.begin(), wire(30));
    expect(useQueueSettingsCache.getState().unsure).toEqual([]);
    expect(useQueueSettingsCache.getState().queues[0].spendCapUsd).toBe(30);
  });

  it("is settled by a saved row of that category too", () => {
    const c = useQueueSettingsCache.getState();
    c.reset();
    c.publish(queueScope(), c.begin(), wire(60));
    c.markUnsure(queueScope(), "c1");
    useQueueSettingsCache.getState().applySaved(queueScope(), row(30));
    expect(useQueueSettingsCache.getState().unsure).toEqual([]);
  });
});

describe("an unknown outcome answered after a scope switch", () => {
  it("does not mark the scope now shown", () => {
    const c = useQueueSettingsCache.getState();
    c.reset();
    c.publish(queueScope(), c.begin(), { tenantEnabled: true, repoSettings: true, queues: [] });
    const inFlight = c.begin();
    c.markUnsure("another-scope", "c1");
    expect(useQueueSettingsCache.getState().unsure).toEqual([]);
    // The read of the scope shown still lands.
    c.publish(queueScope(), inFlight, { tenantEnabled: true, repoSettings: true, queues: [readQueue({ categoryId: "c9" })!] });
    expect(useQueueSettingsCache.getState().queues.map((q) => q.categoryId)).toEqual(["c9"]);
  });
});
