// Delete from the selection bar. What matters: a claimed memo is never sent to the delete
// endpoint, a refused delete does not abort the rest of the batch, replaced rows in an open
// fold are selectable, and the image sweep runs once per batch from a fresh list.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
const refuse = new Set<string>();
const memoDelete = vi.fn(async (id: string) => ({ ok: !refuse.has(id) }));
const memoImageGC = vi.fn(async (_keep: string[]) => ({}));
const memoList = vi.fn(async () => servedMemos);

vi.mock("./api.ts", () => ({
  memoList: () => memoList(),
  memoCategoryList: async () => [{ id: "c1", repo: "", name: "inbox-q", position: 0, createdAt: "2026-09-09T00:00:00Z" } as MemoCategory],
  memoCreate: async () => ({}),
  memoUpdate: async () => ({}),
  memoDelete: (id: string) => memoDelete(id),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: (keep: string[]) => memoImageGC(keep),
  memoQueueList: async () => ({
    tenantEnabled: true,
    queues: [{ categoryId: "c1", repo: "", category: "inbox-q", instruction: "x", agentKind: "claude", route: "", worktree: true, maxActive: 1, batch: 1, maxTaskHours: 12, maxFiresPerDay: 20, enabled: true }],
  }),
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

let root: Root | null = null;
let host: HTMLDivElement;

const aMemo = (over: Partial<Memo>): Memo => ({
  id: "m1",
  repo: "",
  category: "inbox-q",
  kind: "text",
  body: "note",
  refPath: "",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  sentAt: "",
  ...over,
});

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}
async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <MemoQueueSection />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}
const q = <T extends Element>(sel: string): T => {
  const el = document.querySelector<T>(sel);
  if (!el) throw new Error(`not in the DOM: ${sel}`);
  return el;
};

beforeEach(() => {
  localStorage.clear();
  refuse.clear();
  memoDelete.mockClear();
  memoImageGC.mockClear();
  memoList.mockClear();
  servedMemos = [
    aMemo({ id: "p1", state: "pending", body: "pending one" }),
    aMemo({ id: "d1", state: "delivered", sentAt: "2026-09-10T00:00:00Z", body: "delivered one" }),
    aMemo({ id: "r1", state: "superseded", body: "replaced one" }),
    aMemo({ id: "c1x", state: "claimed", body: "claimed one" }),
  ];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("deleting the selected memos", () => {
  it("deletes all but the claimed one, reports it, and sweeps images once", async () => {
    await render();
    await click(q(".memo-sent-fold"));
    // Select-all on the category head covers the open fold, replaced row included.
    await click(q(".memo-cat-check"));
    expect(q(".memo-selbar-n").textContent).toContain("4");
    const del = [...document.querySelectorAll<HTMLButtonElement>(".memo-selbar .ui-btn-danger")][0];
    await click(del);
    // The confirmation names the count.
    expect(q(".ui-confirm-body").textContent).toContain("4");
    await click(q(".ui-confirm-actions .ui-btn-danger"));
    expect(memoDelete.mock.calls.map((c) => c[0]).sort()).toEqual(["d1", "p1", "r1"]);
    expect(memoImageGC).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toMatch(/Deleted 3|3 件を削除/);
    expect(document.body.textContent).toMatch(/1 could not|1 件は削除できません/);
  });

  it("keeps going when one delete is refused, and still sweeps once", async () => {
    refuse.add("p1");
    await render();
    await click(q(".memo-sent-fold"));
    await click(q(".memo-cat-check"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".memo-selbar .ui-btn-danger")][0]);
    await click(q(".ui-confirm-actions .ui-btn-danger"));
    expect(memoDelete.mock.calls.map((c) => c[0]).sort()).toEqual(["d1", "p1", "r1"]);
    expect(memoImageGC).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).toMatch(/2 could not|2 件は削除できません/);
  });

  it("does nothing when the confirmation is cancelled", async () => {
    await render();
    await click(q(".memo-cat-check"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".memo-selbar .ui-btn-danger")][0]);
    await click(q(".ui-confirm-actions .ui-btn:not(.ui-btn-danger)"));
    expect(memoDelete).not.toHaveBeenCalled();
    expect(memoImageGC).not.toHaveBeenCalled();
  });

  it("does not sweep when every delete failed", async () => {
    for (const id of ["p1", "d1"]) refuse.add(id);
    servedMemos = servedMemos.slice(0, 2);
    await render();
    await click(q(".memo-cat-check"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".memo-selbar .ui-btn-danger")][0]);
    await click(q(".ui-confirm-actions .ui-btn-danger"));
    expect(memoImageGC).not.toHaveBeenCalled();
  });
});
