// The background review inside Settings > Agent memory (ADR 0117): the count beside "Changes", the
// "Proposed" sub-list under Changes (offered while the review is on or proposals are waiting), and
// landing on it when something is waiting.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));

const { MemoryTab } = await import("./MemoryTab.tsx");
const { useSettingsUI } = await import("../store.ts");
const { setSettings, settingsDefaults } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const flush = () =>
  act(async () => {
    for (let i = 0; i < 6; i++) await Promise.resolve();
  });

let status: unknown;
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<MemoryTab />));
  await flush();
}
const tab = (id: string) => host!.querySelector<HTMLButtonElement>(`[role=tab][id="${id}"]`);
const click = async (el: Element | null) => {
  expect(el).toBeTruthy();
  await act(async () => (el as HTMLElement).click());
  await flush();
};

beforeEach(() => {
  localStorage.clear();
  setSettings(settingsDefaults());
  status = { mode: "propose", enabled: true, pending: 3 };
  api.mockImplementation((path: string) => {
    if (path === "api/agents/memory-review") return Promise.resolve(status);
    if (path.startsWith("api/agents/memory/entries/changes")) return Promise.resolve({ changes: [] });
    return Promise.resolve({});
  });
  apiJSON.mockImplementation((path: string) =>
    Promise.resolve(path === "api/agents/memory-review/proposals/list" ? { proposals: [] } : {}),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => useSettingsUI.getState().closeSettings());
  api.mockReset();
  apiJSON.mockReset();
  document.body.innerHTML = "";
});

describe("the review in Agent memory > Changes", () => {
  it("shows the waiting count beside Changes, named for a screen reader", async () => {
    await mount();
    const changes = tab("mem-af-tab-changes")!;
    expect(changes.querySelector(".mem-viewnav-count")?.textContent).toBe("3");
    expect(changes.getAttribute("aria-label")).toMatch(/3/);
    expect(tab("mem-af-tab-memories")!.querySelector(".mem-viewnav-count")).toBeNull();
  });

  it("shows no count and no sub-list while the review is off and nothing waits", async () => {
    status = { mode: "off", enabled: false, pending: 0 };
    await mount();
    expect(tab("mem-af-tab-changes")!.querySelector(".mem-viewnav-count")).toBeNull();
    expect(tab("mem-af-tab-changes")!.hasAttribute("aria-label")).toBe(false);
    await click(tab("mem-af-tab-changes"));
    expect(host!.querySelector("#mem-af-changes-panel")).toBeNull();
    expect(host!.querySelector(".mem-changes")).toBeTruthy();
  });

  it("treats a status the Agent could not give as no information, not as an empty queue", async () => {
    status = { error: { code: "http_404", message: "x" } };
    await mount();
    expect(tab("mem-af-tab-changes")!.querySelector(".mem-viewnav-count")).toBeNull();
    await click(tab("mem-af-tab-changes"));
    expect(host!.querySelector("#mem-af-changes-panel")).toBeNull();
  });

  it("lands on the proposals when some are waiting, and the member can go to the change log", async () => {
    await mount();
    await click(tab("mem-af-tab-changes"));
    expect(tab("mem-af-changes-tab-proposed")?.getAttribute("aria-selected")).toBe("true");
    expect(tab("mem-af-changes-tab-proposed")!.querySelector(".mem-viewnav-count")?.textContent).toBe("3");
    expect(apiJSON.mock.calls.some(([p]) => p === "api/agents/memory-review/proposals/list")).toBe(true);
    await click(tab("mem-af-changes-tab-log"));
    expect(tab("mem-af-changes-tab-log")?.getAttribute("aria-selected")).toBe("true");
    expect(api.mock.calls.some(([p]) => String(p).startsWith("api/agents/memory/entries/changes"))).toBe(true);
  });

  it("offers the sub-list while the review is on even with nothing waiting, landing on the change log", async () => {
    status = { mode: "propose", enabled: true, pending: 0 };
    await mount();
    await click(tab("mem-af-tab-changes"));
    expect(tab("mem-af-changes-tab-log")?.getAttribute("aria-selected")).toBe("true");
    expect(tab("mem-af-changes-tab-proposed")!.querySelector(".mem-viewnav-count")).toBeNull();
  });

  it("keeps offering the proposals after the review is switched off, while some are left", async () => {
    status = { mode: "off", enabled: false, pending: 2 };
    await mount();
    await click(tab("mem-af-tab-changes"));
    expect(tab("mem-af-changes-tab-proposed")).toBeTruthy();
  });

  describe("while Proposed stays open", () => {
    const prop = (id: string, name: string) => ({
      id, runId: "aaaaaaaaaaaaaaaaaaaa", session: "s", op: "create", scope: "user", name, reason: "r", evidence: [1],
      created: "2026-10-19T09:00:00Z", state: "pending", diff: "+x",
    });
    let queue: unknown[];
    const names = () => Array.from(host!.querySelectorAll(".mem-list .mem-af-name")).map((e) => e.textContent);
    const tick = async () => {
      await act(async () => {
        vi.advanceTimersByTime(30_000);
      });
      await flush();
    };
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
      queue = [prop("0123456789abcdef0001", "first")];
      status = { mode: "propose", enabled: true, pending: 1 };
      apiJSON.mockImplementation((path: string) =>
        Promise.resolve(path === "api/agents/memory-review/proposals/list" ? { proposals: queue } : {}),
      );
    });
    afterEach(() => vi.useRealTimers());

    it("shows a proposal the background review added, with the count, without reopening", async () => {
      await mount();
      await click(tab("mem-af-tab-changes"));
      expect(names()).toEqual(["first"]);
      queue = [...queue, prop("0123456789abcdef0002", "second")];
      status = { mode: "propose", enabled: true, pending: 2 };
      await tick();
      expect(tab("mem-af-changes-tab-proposed")!.querySelector(".mem-viewnav-count")?.textContent).toBe("2");
      expect(names()).toEqual(["first", "second"]);
    });

    it("replaces the list when the count stays the same but the proposals do not, keeping the pick", async () => {
      await mount();
      await click(tab("mem-af-tab-changes"));
      queue = [prop("0123456789abcdef0001", "first"), prop("0123456789abcdef0003", "third")];
      status = { mode: "propose", enabled: true, pending: 2 };
      await tick();
      expect(names()).toEqual(["first", "third"]);
      queue = [prop("0123456789abcdef0001", "first"), prop("0123456789abcdef0004", "fourth")]; // another tab decided one
      await tick();
      expect(names()).toEqual(["first", "fourth"]);
      expect(host!.querySelector(".mem-list .mem-snap.active .mem-af-name")?.textContent).toBe("first");
    });

    it("does not say the queue is empty while the count says otherwise", async () => {
      queue = [];
      status = { mode: "propose", enabled: true, pending: 0 };
      await mount();
      await click(tab("mem-af-tab-changes"));
      await click(tab("mem-af-changes-tab-proposed"));
      expect(host!.querySelector(".mem-list .mem-af-name")).toBeNull();
      queue = [prop("0123456789abcdef0005", "late")];
      status = { mode: "propose", enabled: true, pending: 1 };
      await tick();
      expect(names()).toEqual(["late"]);
    });

    it("keeps the rows on screen, and says so, when a refresh cannot read the queue", async () => {
      await mount();
      await click(tab("mem-af-tab-changes"));
      apiJSON.mockImplementation(() => Promise.resolve({ error: { code: "forbidden", message: "agent says no" } }));
      await tick();
      expect(names()).toEqual(["first"]);
      expect(host!.querySelector(".mem-warn")).toBeTruthy();
    });
  });
});
