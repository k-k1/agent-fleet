// The queue settings are one shared cache that the Memo queue view and the Auto-run view both
// read, and a save writes into it at once: a dialog opened from the other view right after a save
// starts from the saved values, whether the refetch has not answered yet, fails, or still serves
// the old row.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

const aQueue = (over: Record<string, unknown> = {}) => ({
  categoryId: "c1",
  repo: "agent-fleet",
  category: "inbox-q",
  instruction: "triage",
  agentKind: "claude",
  model: "",
  effort: "",
  route: "",
  spendCapUsd: 0,
  worktree: true,
  maxActive: 2,
  batch: 1,
  maxTaskHours: 12,
  openCron: "",
  openMinutes: 0,
  tz: "",
  openAfter: 0,
  maxFiresPerDay: 20,
  enabled: true,
  generation: 1,
  ...over,
});

// How the list read answers: the stored row, an error, or never within the test. (A read that
// does answer after a save is the server's truth and rightly replaces the cache.)
let listMode: "old" | "fail" | "pending" = "old";
const memoQueuePut = vi.fn(async (_id: string, _body: unknown): Promise<unknown> => aQueue({ batch: 3, generation: 2 }));
const memoQueueList = vi.fn(async (): Promise<unknown> => {
  if (listMode === "fail") throw new Error("down");
  if (listMode === "pending") return new Promise(() => {});
  return { tenantEnabled: true, queues: [aQueue()] };
});
const cat: MemoCategory = { id: "c1", repo: "agent-fleet", name: "inbox-q", position: 0, createdAt: "2026-09-09T00:00:00Z" };
vi.mock("./api.ts", () => ({
  memoList: async (): Promise<Memo[]> => [],
  memoCategoryList: async () => [cat],
  memoCreate: async () => ({}),
  memoUpdate: async () => ({}),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: () => memoQueueList(),
  memoRelease: async () => ({}),
  memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body),
  memoQueueDelete: async () => ({ ok: true }),
  memoQueueFire: async () => ({}),
  memoQueueResume: async () => ({}),
  memoQueueDeliveries: async () => [{ id: "d1", session: "run-a", state: "open" }],
  memoQueueDeliveryEnd: async () => ({}),
  memoQueueDeliveryReturn: async () => ({}),
  memoTidyRuns: async () => ({ runs: [], usedToday: 0, dailyCap: 10 }),
  memoTidyUndo: async () => ({}),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { AutoRunSection } = await import("../autorun/AutoRunSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ViewShownContext } = await import("../../lib/viewShown.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useQueueSettingsCache } = await import("./queueSettingsCache.ts");
const { t } = await import("../../lib/i18n/index.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let memoRoot: Root;
let autoRoot: Root;
let memoHost: HTMLDivElement;
let autoHost: HTMLDivElement;

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}
async function setValue(el: HTMLInputElement, v: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const gear = (host: HTMLElement, cls: string) => {
  const el = host.querySelector<HTMLElement>(`${cls}[aria-label="${t("memo.autorun_settings")}"]`);
  if (!el) throw new Error("no gear in " + cls);
  return el;
};
const dialog = () => document.querySelector<HTMLElement>(".memo-q-modal");
const batch = () => document.querySelector<HTMLInputElement>("#memo-q-batch")?.value;

/** Saves batch=3 from the dialog the given gear opens; the dialog closes itself on success. */
async function saveFrom(host: HTMLElement, cls: string): Promise<void> {
  await click(gear(host, cls));
  await setValue(document.querySelector<HTMLInputElement>("#memo-q-batch")!, "3");
  await click(dialog()!.querySelector("button[type=submit]")!);
  expect(memoQueuePut).toHaveBeenCalledTimes(1);
  expect(dialog()).toBeNull();
}

beforeEach(async () => {
  useQueueSettingsCache.getState().reset();
  listMode = "old";
  memoQueuePut.mockClear();
  memoQueueList.mockClear();
  useSessionsStore.setState({
    sessions: [{ name: "run-a", title: "run-a", kind: "claude", alive: true, origin: "queue", createdAt: new Date().toISOString() }],
  });
  memoHost = document.createElement("div");
  autoHost = document.createElement("div");
  document.body.append(memoHost, autoHost);
  memoRoot = createRoot(memoHost);
  autoRoot = createRoot(autoHost);
  await act(async () =>
    memoRoot.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoQueueSection />
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await act(async () =>
    autoRoot.render(
      <ToastProvider>
        <ConfirmProvider>
          <ViewShownContext.Provider value={true}>
            <AutoRunSection />
          </ViewShownContext.Provider>
        </ConfirmProvider>
      </ToastProvider>,
    ),
  );
  await settle();
});
afterEach(async () => {
  await act(async () => {
    memoRoot.unmount();
    autoRoot.unmount();
  });
  memoHost.remove();
  autoHost.remove();
  useSessionsStore.setState({ sessions: [] });
});

describe.each(["pending", "fail"] as const)("a save, then the refetch is %s", (mode) => {
  it("a dialog opened in the Memo queue view after a save in the Auto-run view starts from the saved values", async () => {
    listMode = mode;
    await saveFrom(autoHost, ".autorun-cat-act");
    await click(gear(memoHost, ".memo-cat-act"));
    expect(batch()).toBe("3");
  });

  it("a dialog opened in the Auto-run view after a save in the Memo queue view starts from the saved values", async () => {
    listMode = mode;
    await saveFrom(memoHost, ".memo-cat-act");
    await click(gear(autoHost, ".autorun-cat-act"));
    expect(batch()).toBe("3");
  });
});

it("a read the other view had in flight before a save cannot put the old values back", async () => {
  // The Auto-run view's read starts before the save and is not restarted by a save made in the
  // Memo queue view, so only the cache's ordering keeps it from landing the old row afterwards.
  let release: (v: unknown) => void = () => {};
  memoQueueList.mockImplementationOnce(() => new Promise((r) => (release = r)));
  await click(autoHost.querySelector(`[aria-label="${t("autorun.reload")}"]`)!);
  listMode = "pending"; // the reads after the save stay out
  await saveFrom(memoHost, ".memo-cat-act");
  await act(async () => release({ tenantEnabled: true, queues: [aQueue()] }));
  await settle();
  await click(gear(autoHost, ".autorun-cat-act"));
  expect(batch()).toBe("3");
});
