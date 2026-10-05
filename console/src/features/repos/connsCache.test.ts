import { describe, expect, it, vi } from "vitest";
import { clearCachedConns, getCachedConns, setCachedConns, subscribeConns } from "./connsCache.ts";
import type { ConnectionsStatus } from "../../types/session.ts";

const snapshot = { claude: { connected: true } } as unknown as ConnectionsStatus;

describe("connsCache", () => {
  it("forgets the snapshot and tells readers, so a switch does not keep the old tenant's agents", () => {
    setCachedConns(snapshot);
    const seen = vi.fn();
    const off = subscribeConns(seen);
    clearCachedConns();
    expect(getCachedConns()).toBeNull();
    expect(seen).toHaveBeenCalledTimes(1);
    clearCachedConns(); // already empty: nothing to announce
    expect(seen).toHaveBeenCalledTimes(1);
    off();
  });
});
