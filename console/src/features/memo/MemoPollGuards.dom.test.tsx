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

const mount = () =>
  act(async () =>
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoQueueSection />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );

describe("MemoQueueSection poll guards", () => {
  it("drops the list and queue settings of the previous tenant while the new tenant's read is still out", async () => {
    await act(async () => useTenantStore.setState({ tenant: "A" } as never));
    await mount();
    await act(async () => useTenantStore.setState({ tenant: "B" } as never));
    expect(memoReads).toHaveLength(2);
    expect(queueReads).toHaveLength(2);
    // Only the generation can tell A's answers apart here: B has not landed, so neither the list
    // sequence nor the settings cache floor has moved.
    await act(async () => memoReads[0]!.resolve([aMemo("a", "memo of A")]));
    await act(async () => queueReads[0]!.resolve(queue("catA")));
    expect(host.textContent).not.toContain("memo of A");
    expect(useQueueSettingsCache.getState().queues).toEqual([]);
    await act(async () => memoReads[1]!.resolve([aMemo("b", "memo of B")]));
    await act(async () => queueReads[1]!.resolve(queue("catB")));
    expect(host.textContent).toContain("memo of B");
    expect(useQueueSettingsCache.getState().queues.map((q) => q.categoryId)).toEqual(["catB"]);
  });

  it("keeps polling on the original beat after a switch, with the new tenant's inputs", async () => {
    await mount();
    await act(async () => void vi.advanceTimersByTime(5000));
    await act(async () => useTenantStore.setState({ tenant: "B" } as never));
    expect(memoReads).toHaveLength(2);
    await act(async () => void vi.advanceTimersByTime(5000)); // the original period ends here
    expect(memoReads).toHaveLength(3);
    expect(queueReads).toHaveLength(3);
    await act(async () => memoReads[2]!.resolve([aMemo("b", "memo of B")]));
    await act(async () => queueReads[2]!.resolve(queue("catB")));
    expect(useQueueSettingsCache.getState().tenant.startsWith("B")).toBe(true);
    // The switch's own read answers late with older content: it must not roll the tick's list back.
    await act(async () => memoReads[1]!.resolve([aMemo("b0", "stale memo of B")]));
    expect(host.textContent).toContain("memo of B");
    expect(host.textContent).not.toContain("stale memo of B");
  });
});
