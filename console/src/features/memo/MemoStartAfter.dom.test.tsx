// A start time a note of an auto-run category carries (ADR 0137, addendum 2026-11-22): the row and
// the approval card say when it may run, and the edit view sets, keeps or clears it, read in the
// member's time zone setting and sent only when the member changed it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";
import { setFormatPrefs } from "../../lib/intl.ts";

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
const { fmtStartAfter } = await import("./queues.ts");

let root: Root | null = null;
let host: HTMLDivElement;

// Far enough ahead to stay ahead whenever the test runs.
const START = "2099-12-04T00:00:00.000000Z";

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
const queueCat: MemoCategory = { id: "c1", repo: "", name: "inbox", position: 0, createdAt: "", queue: true };

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
const rowOf = (text: string) => [...document.querySelectorAll<HTMLElement>(".memo-row")].find((r) => r.textContent?.includes(text))!;
const btn = (r: HTMLElement, label: string) => [...r.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
const openEditor = async (text: string) => {
  for (let i = 0; i < 2 && !document.querySelector(".memo-row.editing"); i++) await click(rowOf(text).querySelector<HTMLElement>(".memo-body")!);
};
const editRow = () => document.querySelector<HTMLElement>(".memo-row.editing")!;
const startInput = () => editRow().querySelector<HTMLInputElement>(".memo-edit-start-input")!;
// React tracks an input's value, so a change goes through the native setter.
const typeInto = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
};

beforeEach(() => {
  localStorage.clear();
  setFormatPrefs("en-US", "Asia/Tokyo");
  servedMemos = [];
  servedCats = [queueCat];
  memoUpdate.mockClear();
  memoUpdate.mockResolvedValue({});
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  setFormatPrefs("auto", "auto");
});

describe("a note with a start time", () => {
  it("says on its row when it may run, and not once the time has passed", async () => {
    servedMemos = [
      aMemo({ id: "a", body: "later job", state: "pending", startAfter: START }),
      aMemo({ id: "b", body: "past job", state: "pending", position: 1, startAfter: "2020-01-01T00:00:00.000000Z" }),
    ];
    await renderSection();
    expect(rowOf("later job").querySelector(".memo-scheduled")?.textContent).toBe(t("memo.start_until", { at: fmtStartAfter(START) }));
    expect(rowOf("past job").querySelector(".memo-scheduled")).toBeNull();
  });

  it("shows the stored time in the member's time zone, and sends it only when changed", async () => {
    servedMemos = [aMemo({ id: "a", body: "later job", state: "pending", startAfter: START })];
    await renderSection();
    await openEditor("later job");
    // 00:00 UTC is 09:00 in Tokyo, the zone the member chose, whatever the browser's is.
    expect(startInput().value).toBe("2099-12-04T09:00");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "later job", category: "inbox" });

    await openEditor("later job");
    await typeInto(startInput(), "2099-12-05T18:30");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "later job", category: "inbox", startAfter: "2099-12-05T09:30:00.000Z" });

    await openEditor("later job");
    await click(btn(editRow(), t("memo.start_clear")));
    expect(startInput().value).toBe("");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "later job", category: "inbox", startAfter: "" });
  });

  it("does not clear a time set elsewhere while the edit was open", async () => {
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending" })];
    await renderSection();
    await openEditor("job");
    expect(startInput().value).toBe("");
    // Another tab sets a time; the list refresh brings it while this edit is still open.
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending", startAfter: START })];
    const { useMemoStore } = await import("./store.ts");
    await act(async () => useMemoStore.getState().bump());
    await settle();
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "job", category: "inbox" });
  });

  it("keeps the zone it opened with when the setting changes before the save", async () => {
    setFormatPrefs("en-US", "America/New_York");
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending" })];
    await renderSection();
    await openEditor("job");
    await typeInto(startInput(), "2099-12-04T09:00");
    setFormatPrefs("en-US", "Asia/Tokyo");
    await click(btn(editRow(), t("memo.save")));
    // 09:00 in New York, as the member typed it, not 09:00 in Tokyo.
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "job", category: "inbox", startAfter: "2099-12-04T14:00:00.000Z" });
  });

  it("reads the field in the zone it shows, even when the setting changed before the first input", async () => {
    setFormatPrefs("en-US", "Asia/Tokyo");
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending" })];
    await renderSection();
    await openEditor("job");
    setFormatPrefs("en-US", "America/New_York");
    expect(editRow().querySelector(".memo-edit-start")!.textContent).toContain("Asia/Tokyo");
    await typeInto(startInput(), "2099-12-04T09:00");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "job", category: "inbox", startAfter: "2099-12-04T00:00:00.000Z" });
  });

  it("keeps the typed time when the note's text is refreshed while the edit is open", async () => {
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending" })];
    await renderSection();
    await openEditor("job");
    await typeInto(startInput(), "2099-12-04T09:00");
    servedMemos = [aMemo({ id: "a", body: "job, edited elsewhere", state: "pending" })];
    const { useMemoStore } = await import("./store.ts");
    await act(async () => useMemoStore.getState().bump());
    await settle();
    expect(startInput().value).toBe("2099-12-04T09:00");
  });

  it("tells apart the two instants of a wall time the clocks repeat", async () => {
    setFormatPrefs("en-US", "America/New_York");
    servedMemos = [
      aMemo({ id: "a", body: "first", state: "pending", startAfter: "2099-11-01T05:30:00.000000Z" }),
      aMemo({ id: "b", body: "second", state: "pending", position: 1, startAfter: "2099-11-01T06:30:00.000000Z" }),
    ];
    await renderSection();
    const first = rowOf("first").querySelector(".memo-scheduled")!.textContent;
    const second = rowOf("second").querySelector(".memo-scheduled")!.textContent;
    expect(first).toContain("EDT");
    expect(second).toContain("EST");
    expect(first).not.toBe(second);
  });

  it("refuses a wall time the clocks skip, and saves nothing", async () => {
    setFormatPrefs("en-US", "America/New_York");
    servedMemos = [aMemo({ id: "a", body: "job", state: "pending" })];
    await renderSection();
    await openEditor("job");
    await typeInto(startInput(), "2027-03-14T02:30");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).not.toHaveBeenCalled();
    expect(editRow().querySelector('[role="alert"]')?.textContent).toBe(t("memo.start_invalid"));
    await typeInto(startInput(), "2027-03-14T03:30");
    expect(editRow().querySelector('[role="alert"]')).toBeNull();
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "job", category: "inbox", startAfter: "2027-03-14T07:30:00.000Z" });
  });

  it("offers no start time outside an auto-run category", async () => {
    servedCats = [{ ...queueCat, queue: false }];
    servedMemos = [aMemo({ id: "a", body: "plain note" })];
    await renderSection();
    await openEditor("plain note");
    expect(editRow().querySelector(".memo-edit-start")).toBeNull();
  });
});

describe("the approval card", () => {
  it("shows the start time, which the approval covers", async () => {
    const m = aMemo({ id: "d1", body: "check the keys", state: "held", provenance: "workspace", producer: "kid01", startAfter: START });
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
    expect(document.querySelector(".memo-release-after")?.textContent).toBe(t("memo.start_until", { at: fmtStartAfter(START) }));
  });
});
