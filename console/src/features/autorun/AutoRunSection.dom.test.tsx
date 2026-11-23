// The Auto-run view: one row per run under its queue's category, the run's own sessions one
// level below, finished runs folded, and a click opening the session.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

const opened = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => opened(n) }));
const memoQueueList = vi.fn(async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] }));
const memoQueueDeliveries = vi.fn(async (_id: string, _openToo?: boolean): Promise<unknown> => [
  { id: "d1", session: "run-a", state: "open" },
  { id: "d0", session: "run-old", state: "closed", sentAt: "2026-11-01T00:00:00Z", closedAt: "2026-11-01T00:45:00Z" },
]);
const memoQueueFire = vi.fn(async (_id: string): Promise<unknown> => ({ id: "d9" }));
vi.mock("../memo/api.ts", () => ({
  memoQueueList: () => memoQueueList(),
  memoQueueDeliveries: (id: string, openToo?: boolean) => memoQueueDeliveries(id, openToo),
  memoQueueFire: (id: string) => memoQueueFire(id),
}));

// The settings dialog is the Memo queue's own and has its own tests; here only that the gear opens it
// for the category.
vi.mock("../memo/MemoQueueSettingsModal.tsx", () => ({
  MemoQueueSettingsModal: (p: { categoryId: string; queue: unknown; onChanged: () => void }) => (
    <div data-testid="settings-modal" data-cat={p.categoryId} data-has-queue={String(!!p.queue)}>
      <button type="button" data-testid="settings-save" onClick={p.onChanged} />
    </div>
  ),
}));

// SessionMenu hosts the share dialog itself; a marker stands in for the form.
vi.mock("../sharing/ShareCreateModal.tsx", () => ({
  ShareCreateModal: (p: { initialTarget?: string }) => <div data-testid="share-modal" data-target={p.initialTarget} />,
}));

const { AutoRunSection } = await import("./AutoRunSection.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { ViewShownContext } = await import("../../lib/viewShown.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");
const { useQueueSettingsCache } = await import("../memo/queueSettingsCache.ts");
const { useAutoRunStore } = await import("./store.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { singlePaneLayout } = await import("../../layout/ops.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });

const mount = async (shown = true) => {
  await act(async () =>
    root.render(
      <ViewShownContext.Provider value={shown}>
        <ConfirmProvider>
          <ToastProvider>
            <AutoRunSection />
          </ToastProvider>
        </ConfirmProvider>
      </ViewShownContext.Provider>,
    ),
  );
  await act(async () => {
    await Promise.resolve();
  });
};

beforeEach(() => {
  useQueueSettingsCache.getState().reset();
  // The store keeps what a scope knew across a pause of the poll; each test starts without it.
  useAutoRunStore.setState({ queues: [], deliveries: new Map(), loaded: false, scope: "" });
  useLayoutStore.setState({ layout: singlePaneLayout({ kind: "terminal" } as never, null) });
  opened.mockReset();
  memoQueueList.mockReset();
  memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds" }] });
  memoQueueFire.mockClear();
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

  it.each([
    [{ state: "open", draft: true }, "draft", "ui-badge"],
    [{ state: "open" }, "open", "ui-badge-ok"],
    [{ state: "merged" }, "merged", "autorun-pr-merged"],
    [{ state: "closed" }, "closed", "autorun-pr-closed"],
  ])("renders the PR state %j as a badge", async (pr, kind, cls) => {
    useSessionsStore.setState({
      sessions: useSessionsStore.getState().sessions.map((x) => (x.name === "run-a" ? { ...x, pr: { number: 30, url: "u", ...pr } as never } : x)),
    });
    await mount();
    const badge = host.querySelector('[data-autorun-run="run-a"] .autorun-pr .ui-badge')!;
    expect(badge.getAttribute("data-pr-state")).toBe(kind);
    expect(badge.classList.contains(cls)).toBe(true);
    expect(host.querySelector('[data-autorun-run="run-a"] .autorun-pr-n')!.textContent).toBe("#30");
  });

  it("scopes the merged/closed tones above .ui-badge's specificity (autorun.css loads before ui.css)", async () => {
    const { readFileSync } = await import("node:fs");
    const css = readFileSync("src/features/autorun/autorun.css", "utf8");
    expect(css).toMatch(/^\.autorun-pr \.autorun-pr-merged \{/m);
    expect(css).toMatch(/^\.autorun-pr \.autorun-pr-closed \{/m);
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

  it("asks for every open run too, so one older than the newest fifty keeps its category", async () => {
    await mount();
    expect(memoQueueDeliveries).toHaveBeenCalledWith("c1", true);
  });

  describe("the session open in the focused pane", () => {
    const focus = (name: string | null) => act(async () => useLayoutStore.setState({ layout: singlePaneLayout({ kind: "terminal" } as never, name) }));
    const marked = () => [...host.querySelectorAll("[aria-current='true']")];

    it("marks its run row, a child's row, and follows pane focus", async () => {
      await mount();
      await focus("run-a");
      expect(marked()).toEqual([host.querySelector('[data-autorun-run="run-a"] .autorun-run-main')]);
      expect(marked()[0].classList.contains("active")).toBe(true);
      await focus("rev-a");
      expect(marked()).toEqual([host.querySelector('[data-autorun-run="run-a"] .autorun-kid')]);
      await focus("hand");
      expect(marked()).toEqual([]);
    });

    it("scrolls the row into view without moving focus, and leaves the Ended fold closed", async () => {
      const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
      try {
        await mount();
        await focus("run-a");
        await act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
        expect(scroll).toHaveBeenCalledWith({ block: "nearest" });
        expect(document.activeElement).toBe(document.body);
        scroll.mockClear();
        await focus("run-old");
        await act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
        expect(host.querySelector('[data-autorun-run="run-old"]')).toBeNull();
        expect(scroll).not.toHaveBeenCalled();
      } finally {
        scroll.mockRestore();
      }
    });
  });

  it("scrolls once the active row appears late, and not again on a plain re-poll", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    const all = useSessionsStore.getState().sessions;
    try {
      useSessionsStore.setState({ sessions: [] });
      await mount();
      await act(async () => useLayoutStore.setState({ layout: singlePaneLayout({ kind: "terminal" } as never, "run-a") }));
      await act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
      expect(scroll).not.toHaveBeenCalled();
      await act(async () => useSessionsStore.setState({ sessions: all }));
      await act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
      expect(scroll).toHaveBeenCalledTimes(1);
      await act(async () => useSessionsStore.setState({ sessions: all.map((x) => ({ ...x })) }));
      await act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
      expect(scroll).toHaveBeenCalledTimes(1);
    } finally {
      scroll.mockRestore();
    }
  });

  it("scrolls again when the row changes place (category, Ended to Running) but not on a same-content re-poll", async () => {
    const scroll = vi.spyOn(Element.prototype, "scrollIntoView");
    const frame = () => act(async () => new Promise<void>((r) => requestAnimationFrame(() => r())));
    try {
      await mount();
      await act(async () => useLayoutStore.setState({ layout: singlePaneLayout({ kind: "terminal" } as never, "run-old") }));
      await frame();
      // A finished run sits in the closed Ended fold: nothing to scroll to.
      expect(scroll).not.toHaveBeenCalled();
      const base = useSessionsStore.getState().sessions;
      // Resumed: the same run in the same category reaches the Running group.
      await act(async () => useSessionsStore.setState({ sessions: base.map((x) => (x.name === "run-old" ? { ...x, alive: true } : x)) }));
      await frame();
      expect(host.querySelector('[data-autorun-run="run-old"]')).not.toBeNull();
      expect(scroll).toHaveBeenCalledTimes(1);
      await act(async () => useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => ({ ...x })) }));
      await frame();
      expect(scroll).toHaveBeenCalledTimes(1);
      // Its queue settings vanish: the row now renders under "Other runs".
      await act(async () => useQueueSettingsCache.setState({ queues: [] }));
      await frame();
      expect(host.querySelector('[data-autorun-category]')!.getAttribute("data-autorun-category")).not.toBe("agent-fleet / issue-rounds");
      expect(scroll).toHaveBeenCalledTimes(2);
    } finally {
      scroll.mockRestore();
    }
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

  it("files a run whose delivery left the window under its own category, and a run with no category under 'Other runs'", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("gone-a", { origin: "queue", createdAt: "2026-10-01T00:00:00Z", queueCategoryId: "c1" }),
        sess("gone-b", { origin: "queue", createdAt: "2026-10-02T00:00:00Z" }),
        sess("gone-c", { origin: "queue", createdAt: "2026-10-03T00:00:00Z", queueCategoryId: "deleted" }),
      ],
    });
    await mount();
    const cats = [...host.querySelectorAll("[data-autorun-category]")];
    const under = (name: string) => cats.find((c) => c.querySelector(`[data-autorun-run="${name}"]`) !== null) ?? null;
    const catA = under("gone-a");
    expect(catA?.getAttribute("data-autorun-category")).toBe("agent-fleet / issue-rounds");
    const other = under("gone-b");
    expect(other).not.toBeNull();
    expect(other).not.toBe(catA);
    expect(under("gone-c")).toBe(other);
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
    expect(items).toEqual(["Run now", "Run history", "Show in Memo queue"]);
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

  describe("Run now in the category menu", () => {
    const openMenu = async () => {
      await act(async () => host.querySelector<HTMLButtonElement>('button[aria-label="Category actions"]')!.click());
      return [...document.body.querySelectorAll<HTMLButtonElement>(".ui-menu-item")].find((b) => b.textContent!.includes("Run now"))!;
    };
    const queueWith = (extra: Record<string, unknown>) =>
      memoQueueList.mockResolvedValue({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "agent-fleet", category: "issue-rounds", ...extra }] });

    it("starts a run through the same call as the Memo queue view when nothing blocks it", async () => {
      await mount();
      const item = await openMenu();
      expect(item.getAttribute("aria-disabled")).toBeNull();
      await act(async () => item.click());
      expect(memoQueueFire).toHaveBeenCalledWith("c1");
    });

    it("opens the history with the slot holders when the press finds the slots taken", async () => {
      memoQueueFire.mockResolvedValueOnce({ error: { code: "memo_queue_no_slot", message: "x" } });
      await mount();
      const { useMemoStore } = await import("../memo/store.ts");
      await act(async () => (await openMenu()).click());
      expect(useMemoStore.getState().reveal).toMatchObject({ id: "c1", history: true, slotsFull: true });
    });

    it.each([
      ["nothing_waiting", {}, "No memo is waiting to run."],
      ["prerequisites", { blocked: 5 }, "Every waiting memo (5) waits for other memos of this category to finish first."],
      ["no_place", { by: "category" }, "This category is already running as many memos at once as it may. The next one starts when a run finishes."],
    ])("is disabled with the reason as its tooltip: %s", async (reason, detail, words) => {
      queueWith({ fireBlock: { reason, detail } });
      await mount();
      const item = await openMenu();
      expect(item.getAttribute("aria-disabled")).toBe("true");
      expect(item.title).toBe(`Run now is unavailable. ${words}`);
      await act(async () => item.click());
      expect(memoQueueFire).not.toHaveBeenCalled();
    });
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

  describe("session menu", () => {
    const ctx = async (el: Element) => {
      const ev = new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 40, clientY: 50 });
      await act(async () => {
        el.dispatchEvent(ev);
      });
      return ev;
    };
    const sessMenu = () => document.querySelector(".sess-menu");
    const mainBtn = () => host.querySelector<HTMLElement>('[data-autorun-run="run-a"] .autorun-run-main')!;
    const kidBtn = () => host.querySelector<HTMLElement>('[data-autorun-run="run-a"] .autorun-kid')!;

    it("opens the session menu on right-click of a run row and of a child row, not the run menu", async () => {
      await mount();
      expect(sessMenu()).toBeNull();
      const ev = await ctx(mainBtn());
      expect(ev.defaultPrevented).toBe(true);
      expect(sessMenu()).not.toBeNull();
      expect(document.querySelector(".autorun-run-menu[aria-expanded='true']")).toBeNull();
      expect(sessMenu()!.textContent).toContain("run-a");
      await act(async () => {
        document.body.dispatchEvent(new MouseEvent("mousedown", { bubbles: true }));
      });
      expect(sessMenu()).toBeNull();
      await ctx(kidBtn());
      expect(sessMenu()!.textContent).toContain("rev-a");
    });

    it("opens it with the Menu key on a focused row", async () => {
      await mount();
      const ev = new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true });
      await act(async () => {
        kidBtn().dispatchEvent(ev);
      });
      expect(ev.defaultPrevented).toBe(true);
      expect(sessMenu()).not.toBeNull();
    });

    it("keeps the run menu on the row's ... button", async () => {
      await mount();
      await act(async () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-run-menu')!.click());
      expect(document.querySelector(".ui-menu:not(.sess-menu)")).not.toBeNull();
      expect(sessMenu()).toBeNull();
    });

    it("opens it for the other row when another row is right-clicked without closing the first", async () => {
      await mount();
      await ctx(mainBtn());
      expect(sessMenu()!.textContent).toContain("run-a");
      await ctx(kidBtn());
      expect(document.querySelectorAll(".sess-menu").length).toBe(1);
      expect(sessMenu()!.textContent).toContain("rev-a");
      expect(sessMenu()!.textContent).not.toContain("run-a");
    });

    it("opens it with Shift+F10 too", async () => {
      await mount();
      const ev = new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true });
      await act(async () => {
        mainBtn().dispatchEvent(ev);
      });
      expect(ev.defaultPrevented).toBe(true);
      expect(sessMenu()!.textContent).toContain("run-a");
    });

    it("never shows the session menu and the run menu together, by keyboard either way", async () => {
      await mount();
      const dots = () => host.querySelector<HTMLButtonElement>('[data-autorun-run="run-a"] .autorun-run-menu')!;
      const runMenu = () => document.querySelector(".ui-menu:not(.sess-menu)");
      await ctx(mainBtn());
      await act(async () => dots().click());
      expect(runMenu()).not.toBeNull();
      expect(sessMenu()).toBeNull();
      await act(async () => {
        mainBtn().dispatchEvent(new KeyboardEvent("keydown", { key: "ContextMenu", bubbles: true, cancelable: true }));
      });
      expect(sessMenu()).not.toBeNull();
      expect(runMenu()).toBeNull();
    });

    it("keeps a confirmation opened from the menu when its row leaves the list", async () => {
      await mount();
      await ctx(mainBtn());
      const items = Array.from(sessMenu()!.querySelectorAll<HTMLButtonElement>(".ui-menu-item"));
      const recreate = items.find((b) => /recreate/i.test(b.textContent ?? ""))!;
      expect(recreate, items.map((b) => b.textContent).join("|")).toBeTruthy();
      await act(async () => recreate.click());
      const dialog = () => document.querySelector('[role="dialog"], [role="alertdialog"]');
      expect(dialog()).not.toBeNull();
      await act(async () => {
        useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.filter((x) => x.name !== "run-a" && x.name !== "rev-a") });
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull();
      expect(dialog()).not.toBeNull();
    });

    it("keeps a dialog the menu itself hosts, on the same session, after the menu closed and its row left", async () => {
      await mount();
      await ctx(mainBtn());
      const share = Array.from(sessMenu()!.querySelectorAll<HTMLButtonElement>(".ui-menu-item")).find((b) => /share/i.test(b.textContent ?? ""));
      expect(share).toBeTruthy();
      await act(async () => share!.click());
      expect(sessMenu()).toBeNull();
      const modal = () => document.querySelector<HTMLElement>('[data-testid="share-modal"]');
      expect(modal()!.dataset.target).toBe("session:run-a");
      await act(async () => {
        useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.filter((x) => x.name !== "run-a" && x.name !== "rev-a") });
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull();
      expect(modal()!.dataset.target).toBe("session:run-a");
    });

    it("shows the session's current state in an open menu after a store update", async () => {
      await mount();
      await ctx(mainBtn());
      expect(sessMenu()!.textContent).toContain("Stop (resumable");
      await act(async () => {
        useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((x) => (x.name === "run-a" ? { ...x, alive: false } : x)) });
      });
      expect(sessMenu()!.textContent).not.toContain("Stop (resumable");
    });

    it("keeps the menu when the row moves out of the list under it", async () => {
      await mount();
      await ctx(mainBtn());
      expect(sessMenu()).not.toBeNull();
      await act(async () => {
        useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.filter((x) => x.name !== "run-a" && x.name !== "rev-a") });
      });
      expect(host.querySelector('[data-autorun-run="run-a"]')).toBeNull();
      expect(sessMenu()).not.toBeNull();
    });
  });

  it("says so when there is no run", async () => {
    useSessionsStore.setState({ sessions: [sess("hand", { origin: "user" })] });
    await mount();
    expect(host.querySelector(".pane-empty")).not.toBeNull();
  });
});
