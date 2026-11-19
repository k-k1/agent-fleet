// A note that waits for other notes of its auto-run category (ADR 0137, addendum 2026-11-19): what
// its row says it waits for, why it came back for approval, and choosing what it waits for in the
// edit view, sent only when the member changed it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
let servedCats: MemoCategory[] = [];
const memoUpdate = vi.fn(async (_id: string, _patch: unknown): Promise<Record<string, unknown>> => ({}));
vi.mock("./api.ts", () => ({
  memoList: async () => servedMemos,
  memoCategoryList: async () => servedCats,
  memoCreate: async () => ({}),
  memoUpdate: (id: string, patch: unknown) => memoUpdate(id, patch),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: async () => ({ tenantEnabled: true, queues: [] }),
  memoRelease: async () => ({}),
  memoQueueResume: async () => ({}),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { MemoReleaseCard } = await import("./MemoReleaseCard.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const aMemo = (over: Partial<Memo>): Memo => ({
  id: "m1",
  repo: "",
  category: "inbox",
  kind: "text",
  body: "my note",
  refPath: "",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  sentAt: "",
  ...over,
});
const aCat = (over: Partial<MemoCategory>): MemoCategory => ({ id: "c1", repo: "", name: "inbox", position: 0, createdAt: "", ...over });

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function renderSection() {
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
const click = async (el: Element) => {
  await act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
  await settle();
};
const rowOf = (id: string) => [...document.querySelectorAll<HTMLElement>(".memo-row")].find((r) => r.textContent?.includes(id))!;

beforeEach(() => {
  localStorage.clear();
  servedMemos = [];
  servedCats = [aCat({})];
  memoUpdate.mockClear();
  memoUpdate.mockResolvedValue({});
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

const queueCat = aCat({ id: "c1", name: "inbox", queue: true });
const btn = (r: HTMLElement, label: string) => [...r.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
// The first click expands a row, the next one edits it; a row left expanded edits on one.
const openEditor = async (text: string) => {
  for (let i = 0; i < 2 && !document.querySelector(".memo-row.editing"); i++) await click(rowOf(text).querySelector<HTMLElement>(".memo-body")!);
};
const editRow = () => document.querySelector<HTMLElement>(".memo-row.editing")!;

describe("a note that waits for other notes", () => {
  it("says what it waits for and how each stands", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "p1", body: "first job", state: "delivered", position: 0 }),
      aMemo({
        id: "d1",
        body: "second job",
        state: "pending",
        position: 1,
        createdAt: "2026-09-09T00:00:01Z",
        after: ["p1"],
        afterStatus: [{ id: "p1", label: "acme/app#7", state: "waiting", reason: "first_turn" }],
      }),
    ];
    await renderSection();
    const line = rowOf("second job").querySelector(".memo-after")!;
    expect(line.textContent).toContain(t("memo.after_label"));
    expect(line.textContent).toContain("acme/app#7: " + t("memo.after_r_first_turn"));
  });

  it("says which pull request it waits for to merge", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({
        id: "d1",
        body: "second job",
        state: "pending",
        after: ["p1"],
        afterStatus: [
          {
            id: "p1",
            label: "acme/app#7",
            state: "waiting",
            reason: "pr",
            prs: [{ provider: "github", key: "acme/app#12", state: "open", url: "https://github.com/acme/app/pull/12" }],
          },
        ],
      }),
    ];
    await renderSection();
    expect(rowOf("second job").querySelector(".memo-after")!.textContent).toContain(
      "acme/app#7: " + t("memo.after_r_pr_merge", { prs: "#12" }),
    );
  });

  it("says why it came back for approval", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({
        id: "d1",
        body: "second job",
        state: "held",
        after: ["p1"],
        afterStatus: [{ id: "p1", label: "acme/app#7", state: "failed", reason: "aborted" }],
        heldReason: { after: "p1", reason: "aborted" },
      }),
    ];
    await renderSection();
    expect(rowOf("second job").querySelector(".memo-after-held")?.textContent).toBe(
      t("memo.after_held", { label: "acme/app#7", reason: t("memo.after_r_aborted") }),
    );
  });

  it("offers the earlier notes of the category, and sends after only when the member changed it", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "p1", body: "first job", state: "pending", position: 0 }),
      aMemo({ id: "p2", body: "other job", state: "delivered", position: 1 }),
      aMemo({ id: "d1", body: "third job", state: "pending", position: 2, createdAt: "2026-09-09T00:00:01Z", after: ["p2"] }),
      aMemo({ id: "z9", body: "later job", state: "pending", position: 3, createdAt: "2026-09-09T00:00:02Z" }),
    ];
    await renderSection();
    await openEditor("third job");
    const choices = [...editRow().querySelectorAll<HTMLLabelElement>(".memo-edit-after-item")];
    // The notes before it, in the category's order; never itself or a later one.
    expect(choices.map((c) => c.textContent)).toEqual(["first job", "other job"]);
    expect(choices.map((c) => c.querySelector("input")!.checked)).toEqual([false, true]);
    // A text edit alone leaves it out of the patch.
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "third job", category: "inbox" });

    await openEditor("third job");
    await click(editRow().querySelectorAll<HTMLInputElement>(".memo-edit-after-item input")[0]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "third job", category: "inbox", after: ["p1", "p2"] });

    await openEditor("third job");
    await click(editRow().querySelectorAll<HTMLInputElement>(".memo-edit-after-item input")[1]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "third job", category: "inbox", after: [] });
  });

  it("keeps a prerequisite that is no longer on offer visible, so it can be removed, and keeps it while others change", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "p1", body: "first job", state: "pending", position: 0 }),
      aMemo({
        id: "d1",
        body: "second job",
        state: "held",
        position: 1,
        createdAt: "2026-09-09T00:00:01Z",
        after: ["gone1"],
        afterStatus: [{ id: "gone1", label: "acme/app#3", state: "failed", reason: "deleted" }],
        heldReason: { after: "gone1", reason: "deleted" },
      }),
    ];
    await renderSection();
    await openEditor("second job");
    const items = () => [...editRow().querySelectorAll<HTMLLabelElement>(".memo-edit-after-item")];
    expect(items().map((c) => c.querySelector("span")!.textContent)).toEqual(["first job", "acme/app#3"]);
    expect(items()[1].textContent).toContain(t("memo.after_r_deleted"));
    // Ticking another keeps the one already there.
    await click(items()[0].querySelector("input")!);
    expect(items().map((c) => c.querySelector("input")!.checked)).toEqual([true, true]);
    // Unticking it removes it.
    await click(items()[1].querySelector("input")!);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "second job", category: "inbox", after: ["p1"] });
  });

  it("offers nothing outside an auto-run category", async () => {
    servedCats = [aCat({ id: "c1", name: "inbox" })];
    servedMemos = [aMemo({ id: "p1", body: "first job" }), aMemo({ id: "d1", body: "second job", position: 1 })];
    await renderSection();
    await openEditor("second job");
    expect(editRow().querySelector(".memo-edit-after")).toBeNull();
  });

  it("shows the catalogue text when the Control Plane refuses the list", async () => {
    memoUpdate.mockResolvedValue({ error: { code: "memo_after_invalid", message: "english" } });
    servedCats = [queueCat];
    servedMemos = [aMemo({ id: "p1", body: "first job", state: "pending" }), aMemo({ id: "d1", body: "second job", state: "pending", position: 1 })];
    await renderSection();
    await openEditor("second job");
    await click(editRow().querySelectorAll<HTMLInputElement>(".memo-edit-after-item input")[0]);
    await click(btn(editRow(), t("memo.save")));
    expect(document.body.textContent).toContain(t("err.memo_after_invalid"));
  });
});

describe("the approval card", () => {
  it("shows what a note waits for, which the approval covers", async () => {
    const m = aMemo({
      id: "d1",
      body: "second job",
      state: "held",
      provenance: "workspace",
      producer: "kid01",
      after: ["p1"],
      afterStatus: [{ id: "p1", label: "acme/app#7", state: "waiting", reason: "pending" }],
    });
    await act(async () => {
      root!.render(
        <MemoReleaseCard
          card={{ key: "k", provenance: "workspace", producer: "kid01", items: [m] } as never}
          busy={false}
          changedIds={new Set()}
          edited={new Set()}
          awaiting={new Set()}
          onRelease={() => {}}
          onConfirm={() => {}}
        />,
      );
    });
    expect(document.querySelector(".memo-release-after")?.textContent).toBe(t("memo.after_label") + ": acme/app#7");
  });
});
