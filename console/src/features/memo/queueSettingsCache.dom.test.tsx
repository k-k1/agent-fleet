import { describe, expect, it } from "vitest";
import { queueScope, useQueueSettingsCache } from "./queueSettingsCache.ts";
import { readQueue } from "./queues.ts";

describe("a saved queue and its fireBlock", () => {
  const row = (fireBlock: unknown, maxActive = 1) => readQueue({ categoryId: "c1", maxActive, waiting: undefined, fireBlock })!;
  const scope = () => queueScope();

  it("drops a slots-full reason when a save may have raised the limit, and keeps the others", () => {
    const c = useQueueSettingsCache.getState();
    c.reset();
    c.publish(scope(), c.begin(), { tenantEnabled: true, queues: [row({ reason: "no_place", detail: { by: "category" } })] });
    useQueueSettingsCache.getState().applySaved(scope(), row(undefined, 2));
    expect(useQueueSettingsCache.getState().queues[0].fireBlock).toBeUndefined();

    c.reset();
    c.publish(scope(), c.begin(), { tenantEnabled: true, queues: [row({ reason: "prerequisites", detail: { blocked: 2 } })] });
    useQueueSettingsCache.getState().applySaved(scope(), row(undefined, 2));
    expect(useQueueSettingsCache.getState().queues[0].fireBlock?.reason).toBe("prerequisites");
  });
});
