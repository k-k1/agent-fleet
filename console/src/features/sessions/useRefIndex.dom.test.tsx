// useLedgerWhile (#1665): a reference search with the work-items section hidden is the only thing
// asking for the ledger, so a failed first read has to be retried, not given up on.
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
const ok = { items: [], queries: [], sessions: [], fetchedAt: "", running: false };
const fetchMock = vi.fn(async (_input?: RequestInfo | URL) => new Response("{}", { status: 200, headers: { "Content-Type": "application/json" } }));
vi.stubGlobal("fetch", fetchMock);
window.fetch = fetchMock as unknown as typeof window.fetch;

import { useLedgerWhile } from "./useRefIndex.ts";
import { POLL_MS, useWorkItemStore } from "../workitems/store.ts";

function Probe({ active }: { active: boolean }) {
  useLedgerWhile(active);
  return null;
}

let root: Root | null = null;
const render = (active: boolean) => act(() => root!.render(<Probe active={active} />));
const workItemCalls = () => fetchMock.mock.calls.filter((c) => String(c[0]).includes("api/work-items")).length;

beforeEach(() => {
  vi.useFakeTimers();
  fetchMock.mockClear();
  useWorkItemStore.getState().reset();
  root = createRoot(document.createElement("div"));
});
afterEach(() => {
  act(() => root?.unmount());
  vi.useRealTimers();
});

describe("useLedgerWhile", () => {
  it("does nothing until the query is reference-shaped", () => {
    render(false);
    expect(workItemCalls()).toBe(0);
  });

  it("retries after a failed read until the ledger loads, then stops", async () => {
    fetchMock.mockImplementationOnce(async () => {
      throw new Error("offline");
    });
    render(true);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(workItemCalls()).toBe(1);
    expect(useWorkItemStore.getState().loaded).toBe(false);

    fetchMock.mockImplementation(async () => new Response(JSON.stringify(ok), { status: 200, headers: { "Content-Type": "application/json" } }));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(POLL_MS + 1000);
    });
    expect(workItemCalls()).toBe(2);
    expect(useWorkItemStore.getState().loaded).toBe(true);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(3 * POLL_MS);
    });
    expect(workItemCalls()).toBe(2);
  });
});
