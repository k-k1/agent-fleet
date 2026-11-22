// A read started under an older tenant must not land after the tenant changed (#4786): the
// effect-scoped `alive` flag this poller had before moving onto useInterval, as a generation check.
// The interval is no longer restarted by the switch, so the guard is the only thing standing
// between a slow old answer and the new tenant's list and queue settings.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo } from "../../types/memo.ts";

type Deferred = { resolve: (v: unknown) => void };
const memoReads: Deferred[] = [];
const queueReads: Deferred[] = [];
const defer = (into: Deferred[]) => new Promise((resolve) => into.push({ resolve }));

vi.mock("./api.ts", () => ({
  memoList: () => defer(memoReads),
  memoCategoryList: async () => [],
  memoCreate: vi.fn(),
  memoUpdate: vi.fn(),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: vi.fn(),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: () => defer(queueReads),
  memoRelease: async () => ({}),
  memoQueuePut: async () => ({}),
  memoQueueDelete: async () => ({ ok: true }),
  memoQueueFire: async () => ({}),
  memoQueueDeliveries: async () => [],
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useQueueSettingsCache } = await import("./queueSettingsCache.ts");

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  vi.useFakeTimers();
  memoReads.length = 0;
  queueReads.length = 0;
  useQueueSettingsCache.getState().reset();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  await act(async () => useTenantStore.setState({ tenant: "" } as never));
  host.remove();
  vi.useRealTimers();
});

const aMemo = (id: string, body: string): Memo => ({
  id,
  repo: "",
  category: "",
  kind: "text",
  body,
  refPath: "",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  sentAt: "",
});
const queue = (categoryId: string) => ({ tenantEnabled: true, queues: [{ categoryId }] });

describe("MemoQueueSection poll guards", () => {
  it("drops the list and queue settings read under the previous tenant", async () => {
    await act(async () => useTenantStore.setState({ tenant: "A" } as never));
    await act(async () =>
      root.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoQueueSection />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    await act(async () => useTenantStore.setState({ tenant: "B" } as never));
    expect(memoReads).toHaveLength(2);
    expect(queueReads).toHaveLength(2);
    await act(async () => memoReads[1]!.resolve([aMemo("b", "memo of B")]));
    await act(async () => queueReads[1]!.resolve(queue("catB")));
    await act(async () => memoReads[0]!.resolve([aMemo("a", "memo of A")]));
    await act(async () => queueReads[0]!.resolve(queue("catA")));
    expect(host.textContent).toContain("memo of B");
    expect(host.textContent).not.toContain("memo of A");
    expect(useQueueSettingsCache.getState().queues.map((q) => q.categoryId)).toEqual(["catB"]);
  });

  it("polls with the inputs of the latest render after a switch", async () => {
    await act(async () =>
      root.render(
        <ToastProvider>
          <ConfirmProvider>
            <MemoQueueSection />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    await act(async () => useTenantStore.setState({ tenant: "B" } as never));
    expect(memoReads).toHaveLength(2);
    await act(async () => void vi.advanceTimersByTime(10000));
    expect(memoReads).toHaveLength(3);
  });
});
