// The Auto-run view: one row per run under its queue's category, the run's own sessions one
// level below, finished runs folded, and a click opening the session.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const opened = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => opened(n) }));
const memoQueueList = vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] }));
const memoQueueDeliveries = vi.fn(async (_id: string): Promise<unknown> => [
  { id: "d1", session: "run-a", state: "open" },
  { id: "d0", session: "run-old", state: "closed", sentAt: "2026-11-01T00:00:00Z", closedAt: "2026-11-01T00:45:00Z" },
]);
vi.mock("../memo/api.ts", () => ({ memoQueueList: () => memoQueueList(), memoQueueDeliveries: (id: string) => memoQueueDeliveries(id) }));

// The settings dialog is the Memo queue's own and has its own tests; here only that the gear opens it
// for the category.
vi.mock("../memo/MemoQueueSettingsModal.tsx", () => ({
  MemoQueueSettingsModal: (p: { categoryId: string; queue: unknown; onChanged: () => void }) => (
    <div data-testid="settings-modal" data-cat={p.categoryId} data-has-queue={String(!!p.queue)}>
      <button type="button" data-testid="settings-save" onClick={p.onChanged} />
    </div>
  ),
}));

const { AutoRunSection } = await import("./AutoRunSection.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { ViewShownContext } = await import("../../lib/viewShown.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useQueueSettingsCache } = await import("../memo/queueSettingsCache.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });

const mount = async (shown = true) => {
  await act(async () =>
    root.render(
      <ViewShownContext.Provider value={shown}>
        <AutoRunSection />
      </ViewShownContext.Provider>,
    ),
  );
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  useQueueSettingsCache.getState().reset();
  opened.mockReset();
  memoQueueList.mockReset();
  memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] });
  memoQueueDeliveries.mockReset();
  memoQueueDeliveries.mockImplementation(async () => [
    { id: "d1", session: "run-a", state: "open" },
    { id: "d0", session: "run-old", state: "closed", sentAt: "2026-11-01T00:00:00Z", closedAt: "2026-11-01T00:45:00Z" },
  ]);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  useSessionsStore.setState({
    sessions: [
      sess("run-a", {
        origin: "queue",
        createdAt: new Date(Date.now() - 12 * 60_000).toISOString(),
        links: [{ provider: "github", key: "o/r#12", role: "started", title: "Fix it" }],
        pr: { number: 30, state: "open", url: "u" },
      }),
      sess("rev-a", { origin: "session", originSession: "run-a" }),
      sess("run-old", { origin: "queue", alive: false, createdAt: "2026-11-01T00:00:00Z" }),
      sess("hand", { origin: "user" }),
    ],
  });
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});

describe("AutoRunSection", () => {
  it("shows a run row under its category with the item, PR and elapsed time", async () => {
    await mount();
    expect(host.querySelector("[data-autorun-category]")!.getAttribute("data-autorun-category")).toBe("agent-fleet / issue-rounds");
    const row = host.querySelector('[data-autorun-run="run-a"]')!;
    expect(row.getAttribute("data-autorun-state")).toBe("running");
    expect(row.querySelector(".autorun-run-main .autorun-title")!.textContent).toBe("#12 Fix it");
    expect(row.querySelector(".autorun-pr")!.textContent).toContain("#30");
    expect(row.querySelector(".autorun-elapsed")!.textContent).toBe("12m");
  });

  it("opens the run's session, and a session it started, on click", async () => {
    await mount();
    await act(async () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-run-main')!.click());
    expect(opened).toHaveBeenLastCalledWith("run-a");
    await act(async () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-kid')!.click());
    expect(opened).toHaveBeenLastCalledWith("rev-a");
  });

  it("folds ended runs into Ended N and never lists the member's own sessions", async () => {
    await mount();
    expect(host.querySelector('[data-autorun-run="run-old"]')).toBeNull();
    expect(host.querySelector('[data-autorun-run="hand"]')).toBeNull();
    const head = host.querySelector<HTMLButtonElement>(".autorun-sub-head.is-ended")!;
    expect(head.textContent).toContain("1");
    await act(async () => head.click());
    expect(host.querySelector('[data-autorun-run="run-old"]')!.getAttribute("data-autorun-state")).toBe("ended");
  });

  it("keeps a finished run's duration", async () => {
    await mount();
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-sub-head.is-ended")!.click());
    expect(host.querySelector('[data-autorun-run="run-old"] .autorun-elapsed')!.textContent).toBe("45m");
  });

  it("reads nothing while the view is hidden, and reads when it is shown", async () => {
    await mount(false);
    expect(memoQueueList).not.toHaveBeenCalled();
    await mount(true);
    expect(memoQueueList).toHaveBeenCalledTimes(1);
  });

  it("lists a run under 'Other runs' when its category read fails, and keeps the rest", async () => {
    memoQueueDeliveries.mockImplementation(async () => {
      throw new Error("boom");
    });
    await mount();
    expect(host.querySelector('[data-autorun-run="run-a"]')).not.toBeNull();
    expect(host.querySelector("[data-autorun-category]")!.getAttribute("data-autorun-category")).not.toBe("agent-fleet / issue-rounds");
  });

  it("drops what it knew when the tenant changes and reads again", async () => {
    await mount();
    expect(memoQueueList).toHaveBeenCalledTimes(1);
    await act(async () => useTenantStore.setState({ tenant: "other" }));
    expect(memoQueueList).toHaveBeenCalledTimes(2);
    useTenantStore.setState({ tenant: "" });
  });

  it("keeps refreshing one category while another read stays pending", async () => {
    vi.useFakeTimers();
    try {
      memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "a", category: "x" }, { categoryId: "c2", repo: "b", category: "y" }] });
      memoQueueDeliveries.mockImplementation((id: string) => (id === "c2" ? new Promise(() => {}) : Promise.resolve([{ id: "d1", session: "run-a", state: "open" }])));
      await mount();
      expect(memoQueueDeliveries.mock.calls.filter((c) => c[0] === "c1")).toHaveLength(1);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(memoQueueDeliveries.mock.calls.filter((c) => c[0] === "c1")).toHaveLength(2);
      expect(memoQueueDeliveries.mock.calls.filter((c) => c[0] === "c2")).toHaveLength(1); // still in flight
    } finally {
      vi.useRealTimers();
    }
  });

  it("expires an open run that fell off the delivery list, and drops a vanished category", async () => {
    vi.useFakeTimers();
    try {
      await mount();
      expect(host.querySelector('[data-autorun-run="run-a"]')!.getAttribute("data-autorun-state")).toBe("running");
      memoQueueDeliveries.mockImplementation(async () => []);
      useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => (x.origin === "user" ? x : { ...x, alive: false })) });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull(); // no longer pinned open: ended, folded
      memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [{ categoryId: "c9", repo: "z", category: "z" }] });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(host.querySelectorAll("[data-autorun-category]")).toHaveLength(1);
      expect(host.querySelector(".autorun-cat-head")!.textContent).not.toContain("issue-rounds");
    } finally {
      vi.useRealTimers();
    }
  });

  it("lists a closed run whose root is alive under Running with a resumed tag, also after it fell off the list", async () => {
    vi.useFakeTimers();
    try {
      await mount();
      memoQueueDeliveries.mockImplementation(async () => []);
      useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => (x.name === "run-old" ? { ...x, alive: true } : x)) });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      const row = host.querySelector('[data-autorun-run="run-old"]')!;
      expect(row.getAttribute("data-autorun-state")).toBe("running");
      expect(row.closest('[data-autorun-group="live"]')).not.toBeNull();
      expect(row.querySelector(".autorun-resumed")).not.toBeNull();
      expect(host.querySelector('[data-autorun-run="run-a"] .autorun-resumed')).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops an open state of a category that left the queue list, and ignores its late answer", async () => {
    vi.useFakeTimers();
    try {
      await mount();
      memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [] });
      useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => (x.origin === "user" ? x : { ...x, alive: false })) });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull(); // ended, not pinned open
      expect(memoQueueDeliveries).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("ignores a late answer to a read begun before its category was deleted and created again", async () => {
    vi.useFakeTimers();
    try {
      let late: (v: unknown) => void = () => {};
      memoQueueDeliveries.mockImplementationOnce(() => new Promise((r) => (late = r)));
      await mount(); // the first read of c1 stays pending
      const one = { tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] };
      memoQueueList.mockResolvedValueOnce({ tenantEnabled: true, queues: [] }).mockResolvedValue(one);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000); // deleted
      });
      useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => (x.origin === "user" ? x : { ...x, alive: false })) });
      memoQueueDeliveries.mockImplementation(() => new Promise(() => {})); // the new read stays pending
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000); // created again: a new read starts
      });
      expect(memoQueueDeliveries).toHaveBeenCalledTimes(2);
      await act(async () => {
        late([{ id: "d1", session: "run-a", state: "open" }]); // the old answer arrives now
        await Promise.resolve();
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull(); // not pinned open again
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      // The old read's finally must not have cleared the new read's in-flight mark.
      expect(memoQueueDeliveries).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("has a section head with the title, the count of running runs and a reload that reads again", async () => {
    await mount();
    const head = host.querySelector(".ui-section-head")!;
    expect(head.querySelector(".ui-section-title")!.textContent).toBe("Auto-run");
    expect(head.querySelector(".ui-section-count")!.textContent).toBe("1"); // run-a; run-old ended
    await act(async () => head.querySelector<HTMLButtonElement>('button[aria-label="Refresh"]')!.click());
    expect(memoQueueList).toHaveBeenCalledTimes(2);
  });

  it("puts the category head with a repository icon, the gear and the menu in one sticky header", async () => {
    await mount();
    const head = host.querySelector(".autorun-cat-head")!;
    expect(head.querySelector(".codicon-repo")).not.toBeNull();
    expect(head.querySelector(".autorun-cat-name")!.textContent).toBe("agent-fleet / issue-rounds");
    expect(head.querySelector('button[aria-label="Auto-run settings"]')).not.toBeNull();
    await act(async () => head.querySelector<HTMLButtonElement>('button[aria-label="Category actions"]')!.click());
    const items = [...document.body.querySelectorAll(".ui-menu-item")].map((b) => b.textContent!.trim());
    expect(items).toEqual(["Run history", "Show in Memo queue"]);
  });

  it("opens the category's auto-run settings from the gear", async () => {
    await mount();
    expect(host.querySelector('[data-testid="settings-modal"]')).toBeNull();
    await act(async () => host.querySelector<HTMLButtonElement>('.autorun-cat-head button[aria-label="Auto-run settings"]')!.click());
    const m = host.querySelector('[data-testid="settings-modal"]')!;
    expect([m.getAttribute("data-cat"), m.getAttribute("data-has-queue")]).toEqual(["c1", "true"]);
    // A save reads the queues again and tells the Memo queue view, which holds its own copy.
    const { useMemoStore } = await import("../memo/store.ts");
    const tick = useMemoStore.getState().tick;
    const reads = memoQueueList.mock.calls.length;
    await act(async () => host.querySelector<HTMLButtonElement>('[data-testid="settings-save"]')!.click());
    expect(useMemoStore.getState().tick).toBe(tick + 1);
    expect(memoQueueList.mock.calls.length).toBe(reads + 1);
  });

  it("opens the memo queue at the category from the menu", async () => {
    await mount();
    const { useMemoStore } = await import("../memo/store.ts");
    await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="Category actions"]')!.click());
    await act(async () => [...document.body.querySelectorAll<HTMLButtonElement>(".ui-menu-item")].find((b) => b.textContent!.includes("Run history"))!.click());
    expect(useMemoStore.getState().reveal).toMatchObject({ id: "c1", history: true });
  });

  it("has two sub-headers: Running / waiting expanded, Ended folded", async () => {
    await mount();
    const live = host.querySelector<HTMLButtonElement>(".autorun-sub-head.is-live")!;
    expect(live.textContent).toContain("Running / waiting 1");
    expect(live.getAttribute("aria-expanded")).toBe("true");
    expect(host.querySelector<HTMLButtonElement>(".autorun-sub-head.is-ended")!.getAttribute("aria-expanded")).toBe("false");
    await act(async () => live.click());
    expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull();
  });

  it("scopes the categories to the working set, and always lists a category without a repository", async () => {
    memoQueueList.mockResolvedValue({
      tenantEnabled: true,
      queues: [
        { categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" },
        { categoryId: "c2", repo: "", category: "loose" },
      ],
    });
    memoQueueDeliveries.mockImplementation(async (id: string) => (id === "c1" ? [{ id: "d1", session: "run-a", state: "open" }] : [{ id: "d2", session: "run-b", state: "open" }]));
    useSessionsStore.setState({ sessions: [...useSessionsStore.getState().sessions, sess("run-b", { origin: "queue", createdAt: new Date().toISOString() })] });
    const { setSetting } = await import("../../lib/settings.ts");
    setSetting("workingSets", [{ id: "w1", name: "other", repos: ["elsewhere"], convs: [], sessions: [], schedules: [] }]);
    setSetting("workingSetActive", "w1");
    try {
      await mount();
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull();
      expect(host.querySelector('[data-autorun-run="run-b"]')).not.toBeNull();
      setSetting("workingSets", [{ id: "w1", name: "mine", repos: ["agent-fleet"], convs: [], sessions: [], schedules: [] }]);
      await act(async () => {
        await Promise.resolve();
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).not.toBeNull();
    } finally {
      setSetting("workingSetActive", "");
    }
  });

  it("says so when there is no run", async () => {
    useSessionsStore.setState({ sessions: [sess("hand", { origin: "user" })] });
    await mount();
    expect(host.querySelector(".pane-empty")).not.toBeNull();
  });
});
