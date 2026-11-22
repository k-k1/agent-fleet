// Delete from the selection bar. What matters: a claimed memo is never sent to the delete
// endpoint, a refused delete does not abort the rest of the batch, replaced rows in an open
// fold are selectable, and the image sweep runs once per batch from a fresh list.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";
import { headMenuItem } from "./catMenuTest.ts";

let servedMemos: Memo[] = [];
const refuse = new Set<string>();
// A successful delete drops the memo from the list the next memoList() serves, like the server.
const memoDelete = vi.fn(async (id: string) => {
  if (refuse.has(id)) return { ok: false };
  servedMemos = servedMemos.filter((m) => m.id !== id);
  return { ok: true };
});
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
const { t } = await import("../../lib/i18n/index.ts");
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

  it("keeps the images of a memo whose delete was refused and drops the deleted one's", async () => {
    const att = (name: string) => [{ name, path: "/img/" + name } as unknown as NonNullable<Memo["attachments"]>[number]];
    servedMemos = [
      aMemo({ id: "p1", state: "pending", attachments: att("kept.png") }),
      aMemo({ id: "p2", state: "pending", attachments: att("gone.png") }),
    ];
    refuse.add("p1");
    await render();
    await click(q(".memo-cat-check"));
    await click([...document.querySelectorAll<HTMLButtonElement>(".memo-selbar .ui-btn-danger")][0]);
    await click(q(".ui-confirm-actions .ui-btn-danger"));
    expect(memoImageGC).toHaveBeenCalledTimes(1);
    const keep = memoImageGC.mock.calls[0][0];
    expect(keep).toContain("kept.png");
    expect(keep).not.toContain("gone.png");
  });

  it("never offers a replaced note to either Send", async () => {
    await render();
    await click(q(".memo-sent-fold"));
    await click(q(".memo-cat-check"));
    // Selection bar Send: the composed text carries the pending note, not the replaced one.
    await click(q(".memo-selbar .ui-btn-primary"));
    const text = q<HTMLTextAreaElement>(".memo-send-text").value;
    expect(text).toContain("pending one");
    expect(text).not.toContain("replaced one");
  });

  it("lets the head box drop a selection that the closed fold hides", async () => {
    // Only history in the queue: closing the fold leaves nothing on screen to tick or untick.
    servedMemos = servedMemos.filter((m) => m.state === "delivered" || m.state === "superseded");
    await render();
    await click(q(".memo-sent-fold"));
    await click(q(".memo-cat-check"));
    expect(q(".memo-selbar-n").textContent).toContain("2");
    await click(q(".memo-sent-fold"));
    const box = q<HTMLInputElement>(".memo-cat-check");
    expect(box.disabled).toBe(false);
    await click(box);
    expect(document.querySelector(".memo-selbar")).toBeNull();
  });

  it("keeps the head box selecting the visible rows when only a hidden one is picked", async () => {
    servedMemos = servedMemos.filter((m) => m.id === "p1" || m.id === "r1");
    await render();
    await click(q(".memo-sent-fold"));
    const replacedRow = [...document.querySelectorAll<HTMLElement>(".memo-row")].find((r) => r.textContent?.includes("replaced one"));
    await click(replacedRow!.querySelector("input[type=checkbox]")!);
    await click(q(".memo-sent-fold"));
    expect(q(".memo-selbar-n").textContent).toContain("1");
    await click(q(".memo-cat-check"));
    // The pending note is now selected too; the hidden pick is not dropped by that click.
    expect(q(".memo-selbar-n").textContent).toContain("2");
  });

  describe("Clear finished memos", () => {
    it("deletes delivered and replaced memos only, after a confirmation naming the count", async () => {
      await render();
      await click(headMenuItem({ cls: "memo-cat-menu-clear" }));
      expect(q(".ui-confirm-body").textContent).toContain("2");
      await click(q(".ui-confirm-actions .ui-btn-danger"));
      expect(memoDelete.mock.calls.map((c) => c[0]).sort()).toEqual(["d1", "r1"]);
    });

    it("does nothing when cancelled", async () => {
      await render();
      await click(headMenuItem({ cls: "memo-cat-menu-clear" }));
      await click(q(".ui-confirm-actions .ui-btn:not(.ui-btn-danger)"));
      expect(memoDelete).not.toHaveBeenCalled();
    });

    it("keeps a finished memo that another memo still waits for", async () => {
      servedMemos = servedMemos.map((m) => (m.id === "p1" ? { ...m, after: ["d1"] } : m));
      await render();
      await click(headMenuItem({ cls: "memo-cat-menu-clear" }));
      expect(q(".ui-confirm-body").textContent).toContain(t("memo.clear_finished_kept", { count: 1 }));
      await click(q(".ui-confirm-actions .ui-btn-danger"));
      expect(memoDelete.mock.calls.map((c) => c[0])).toEqual(["r1"]);

    });

    it("disables the item when every finished memo is awaited", async () => {
      servedMemos = servedMemos.map((m) => (m.id === "p1" ? { ...m, after: ["d1", "r1"] } : m));
      await render();
      expect(headMenuItem({ cls: "memo-cat-menu-clear" }).getAttribute("aria-disabled")).toBe("true");
    });

    it("is disabled when nothing is finished", async () => {
      servedMemos = servedMemos.filter((m) => m.id === "p1" || m.id === "c1x");
      await render();
      const it = headMenuItem({ cls: "memo-cat-menu-clear" });
      expect(it.getAttribute("aria-disabled")).toBe("true");
      await click(it);
      expect(document.querySelector(".ui-confirm-body")).toBeNull();
      expect(memoDelete).not.toHaveBeenCalled();
    });
  });
});
