// The memo badge against the REAL panel (App mounts it for the whole session): who reads
// api/memos, what a slow or stale answer may do to the count, and what stopping or switching
// tenant clears.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { memoList } = vi.hoisted(() => ({ memoList: vi.fn((): Promise<unknown> => Promise.resolve([])) }));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: () => Promise.resolve({}),
  memoList: () => memoList(),
  memoCategoryList: () => Promise.resolve([]),
  memoQueueList: () => Promise.resolve({ tenantEnabled: false, queues: [] }),
}));
vi.mock("../features/memo/share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));

const { ActivityBar } = await import("./ActivityBar.tsx");
const { MemoQueueSection } = await import("../features/memo/MemoQueueSection.tsx");
const { ToastProvider } = await import("../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../ui/ConfirmProvider.tsx");
const { useMemoStore } = await import("../features/memo/store.ts");
const { useWorkspaceStore } = await import("../core/store/workspace.ts");
const { useTenantStore } = await import("../core/store/tenant.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

const held = (id: string) => ({ id, repo: "", category: "q", kind: "text", body: id, refPath: "", position: 0, createdAt: "", sentAt: "", state: "held" });
const badge = () => host!.querySelector<HTMLElement>('.actbar-btn[data-rail-view="memo"] .actbar-badge');
const flush = () => act(async () => {});
const deferred = () => {
  let res: (v: unknown) => void = () => {};
  const p = new Promise((r) => (res = r));
  return { p, res: (v: unknown) => res(v) };
};

async function mountBoth() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ActivityBar view="sessions" shown foldable schedulerEnabled />
          <MemoQueueSection />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await flush();
}

beforeEach(() => {
  localStorage.clear();
  useMemoStore.setState({ tick: 0, panels: 0, waiting: null, scope: "" });
  useWorkspaceStore.setState({ state: "running" });
  useTenantStore.setState({ tenant: "a" });
  memoList.mockReset();
  memoList.mockImplementation(() => Promise.resolve([held("m1")]));
});
afterEach(async () => {
  vi.useRealTimers();
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
});

describe("memo badge with the panel mounted", () => {
  it("reads api/memos once at start and shows the count", async () => {
    await mountBoth();
    expect(memoList).toHaveBeenCalledTimes(1);
    expect(badge()?.textContent).toBe("1");
  });

  it("has one poll: the bar adds none while the panel is up, and resumes when it is gone", async () => {
    vi.useFakeTimers();
    await mountBoth();
    memoList.mockClear();
    await act(async () => void vi.advanceTimersByTime(10_000));
    expect(memoList).toHaveBeenCalledTimes(1);
    await act(async () =>
      root!.render(
        <ToastProvider>
          <ConfirmProvider>
            <ActivityBar view="sessions" shown foldable schedulerEnabled />
          </ConfirmProvider>
        </ToastProvider>,
      ),
    );
    memoList.mockClear();
    await act(async () => void vi.advanceTimersByTime(10_000));
    expect(memoList).toHaveBeenCalledTimes(2); // an immediate re-read on takeover, then one poll
  });

  it("does not let a slow older answer overwrite a newer one", async () => {
    await mountBoth();
    const slow = deferred();
    memoList.mockImplementationOnce(() => slow.p);
    await act(async () => useMemoStore.getState().bump()); // read A starts, held by the server
    memoList.mockImplementation(() => Promise.resolve([])); // read B
    await act(async () => useMemoStore.getState().bump());
    await flush();
    expect(badge()).toBeNull();
    await act(async () => slow.res([held("m1"), held("m2")])); // A answers last, with old data
    await flush();
    expect(badge()).toBeNull();
  });

  it("clears on stop, and an answer that was in flight when it stopped does not bring it back", async () => {
    await mountBoth();
    expect(badge()?.textContent).toBe("1");
    const late = deferred();
    memoList.mockImplementationOnce(() => late.p);
    await act(async () => useMemoStore.getState().bump());
    await act(async () => useWorkspaceStore.setState({ state: "stopped" }));
    expect(badge()).toBeNull();
    await act(async () => late.res([held("m1"), held("m2")]));
    await flush();
    expect(badge()).toBeNull();
    // The panel keeps reading while stopped; none of it reaches the badge.
    memoList.mockImplementation(() => Promise.resolve([held("m1")]));
    await act(async () => useMemoStore.getState().bump());
    await flush();
    expect(badge()).toBeNull();
  });

  it("shows nothing of the old tenant after a switch, not even for the first render", async () => {
    await mountBoth();
    expect(badge()?.textContent).toBe("1");
    const next = deferred();
    memoList.mockImplementation(() => next.p);
    await act(async () => useTenantStore.setState({ tenant: "b" }));
    expect(badge()).toBeNull();
    await act(async () => next.res([held("x"), held("y")]));
    await flush();
    expect(badge()?.textContent).toBe("2");
  });
});
