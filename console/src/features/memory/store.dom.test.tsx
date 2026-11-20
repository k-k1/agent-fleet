// The proposals feed behind the Memory badge (ADR 0141 decisions 2 and 7): read while the
// workspace runs, retried through the 5xx window right after a start, and dropped on a stop or a
// tenant switch.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
}));

import { pendingProposals, useMemoryStore, useProposalsFeed } from "./store.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

const prop = (id: string, status: string) => ({ id, status, memory: id, items: [], created: "", expires: "" });
function Feed({ running, tenant }: { running: boolean; tenant: string }) {
  useProposalsFeed(running, tenant);
  return null;
}
async function render(running: boolean, tenant = "a") {
  await act(async () => root!.render(<Feed running={running} tenant={tenant} />));
  await act(async () => {});
}

beforeEach(() => {
  vi.useFakeTimers();
  useMemoryStore.setState({ version: 0 });
  useMemoryStore.getState().reset();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
  api.mockReset();
  vi.useRealTimers();
});

describe("useProposalsFeed", () => {
  it("retries a 5xx right after a start instead of waiting for the next poll", async () => {
    api.mockResolvedValueOnce({ error: { code: "http_502", status: 502 } }).mockResolvedValue({ proposals: [prop("a", "pending")] });
    await render(true);
    expect(useMemoryStore.getState().proposals).toBeNull();
    await act(async () => vi.advanceTimersByTime(800));
    await act(async () => {});
    expect(api).toHaveBeenCalledTimes(2);
    expect(pendingProposals(useMemoryStore.getState().proposals)).toBe(1);
  });

  it("reads nothing while stopped and forgets the old answer on a stop", async () => {
    api.mockResolvedValue({ proposals: [prop("a", "pending")] });
    await render(true);
    expect(useMemoryStore.getState().proposals).toHaveLength(1);
    api.mockClear();
    await render(false);
    expect(useMemoryStore.getState().proposals).toBeNull();
    await act(async () => vi.advanceTimersByTime(10 * 60_000));
    expect(api).not.toHaveBeenCalled();
  });

  it("drops one tenant's proposals and reads the next tenant's", async () => {
    api.mockResolvedValue({ proposals: [prop("a", "pending")] });
    await render(true, "a");
    api.mockResolvedValue({ proposals: [] });
    await render(true, "b");
    expect(useMemoryStore.getState().proposals).toEqual([]);
  });

  it("re-reads on a write without restarting the poll", async () => {
    api.mockResolvedValue({ proposals: [] });
    await render(true);
    await act(async () => vi.advanceTimersByTime(4 * 60_000));
    await act(async () => useMemoryStore.getState().bump());
    await act(async () => {});
    expect(api).toHaveBeenCalledTimes(2);
    // The poll was set at mount: it fires one minute after the write, not five.
    await act(async () => vi.advanceTimersByTime(60_000));
    expect(api).toHaveBeenCalledTimes(3);
  });
});
