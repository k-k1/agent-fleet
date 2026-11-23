// A release card approves only the notes ticked on it (#4878): all ticked at first, the button
// says how many, each ticked note goes with its own hash, and the unticked ones stay held there.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
const memoRelease = vi.fn(async (_items: { id: string; contentHash: string }[]): Promise<Record<string, unknown>> => ({}));
vi.mock("./api.ts", () => ({
  memoList: async () => servedMemos,
  memoCategoryList: async () => [{ id: "c1", repo: "", name: "inbox", position: 0, createdAt: "", queue: true }] as MemoCategory[],
  memoCreate: async () => ({}),
  memoUpdate: async () => ({}),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", agentKind: "claude", enabled: true }] }),
  memoRelease: (items: { id: string; contentHash: string }[]) => memoRelease(items),
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
beforeEach(() => {
  localStorage.clear();
  servedMemos = [];
  memoRelease.mockClear();
  memoRelease.mockResolvedValue({});
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

const held = (id: string, body: string): Memo => ({
  id,
  repo: "",
  category: "inbox",
  kind: "text",
  body,
  refPath: "",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  sentAt: "",
  state: "held",
  provenance: "workspace",
  producer: "kid01",
  contentHash: "hash-" + id,
});

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const click = async (el: Element) => {
  await act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true })));
  await settle();
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
const checks = () => [...document.querySelectorAll<HTMLInputElement>(".memo-release-check")];
const approve = () => document.querySelector<HTMLButtonElement>(".memo-release-foot button")!;

describe("the release card's ticks", () => {
  beforeEach(() => {
    servedMemos = [held("a", "start now"), held("b", "wait for a date"), held("c", "start now too")];
  });

  it("ticks every note at first and approves them all, each with its own hash", async () => {
    await renderSection();
    expect(checks().map((c) => c.checked)).toEqual([true, true, true]);
    expect(approve().textContent).toBe(t("memo.release_button", { count: 3 }));
    await click(approve());
    expect(memoRelease).toHaveBeenCalledTimes(1);
    expect(memoRelease.mock.calls[0][0]).toEqual([
      { id: "a", contentHash: "hash-a" },
      { id: "b", contentHash: "hash-b" },
      { id: "c", contentHash: "hash-c" },
    ]);
  });

  it("approves only the ticked notes and says how many", async () => {
    await renderSection();
    await click(checks()[1]);
    expect(checks().map((c) => c.checked)).toEqual([true, false, true]);
    expect(approve().textContent).toBe(t("memo.release_button_partial", { count: 2, total: 3 }));
    await click(approve());
    expect(memoRelease.mock.calls[0][0]).toEqual([
      { id: "a", contentHash: "hash-a" },
      { id: "c", contentHash: "hash-c" },
    ]);
  });

  it("keeps the unticked note on the card, approvable on its own, after the others were approved", async () => {
    memoRelease.mockImplementation(async () => {
      // What the Control Plane serves once a and c are pending: b is all that is held.
      servedMemos = [held("b", "wait for a date")];
      return {};
    });
    await renderSection();
    await click(checks()[1]);
    await click(approve());
    expect(document.querySelectorAll(".memo-release-item")).toHaveLength(1);
    expect(document.querySelector(".memo-release-item")!.textContent).toContain("wait for a date");
    expect(approve().textContent).toBe(t("memo.release_button", { count: 1 }));
    expect(approve().disabled).toBe(false);
    await click(approve());
    expect(memoRelease.mock.calls[1][0]).toEqual([{ id: "b", contentHash: "hash-b" }]);
  });

  it("refuses a stale hash among the ticked notes and approves nothing", async () => {
    memoRelease.mockResolvedValue({ error: { code: "memo_release_stale", message: "x", stale: ["c"] } });
    await renderSection();
    await click(checks()[1]);
    await click(approve());
    expect(memoRelease).toHaveBeenCalledTimes(1);
    expect(memoRelease.mock.calls[0][0].map((i) => i.id)).toEqual(["a", "c"]);
    // The card is still there with every note on it, the stale one marked.
    expect(checks()).toHaveLength(3);
    expect(document.querySelector(".memo-release.changed")).not.toBeNull();
    expect(checks().map((c) => c.checked)).toEqual([true, false, true]);
  });
});

describe("the release card of one note, and of none ticked", () => {
  const card = (items: Memo[]) => ({ key: "k", provenance: "workspace", producer: "kid01", items }) as never;
  const props = { busy: false, changedIds: new Set<string>(), edited: new Set<string>(), awaiting: new Set<string>(), onConfirm: () => {} };

  it("shows no checkbox for a single note and approves it as before", async () => {
    const onRelease = vi.fn();
    await act(async () => {
      root!.render(<MemoReleaseCard card={card([held("a", "only one")])} onRelease={onRelease} {...props} />);
    });
    expect(checks()).toHaveLength(0);
    expect(approve().textContent).toBe(t("memo.release_button", { count: 1 }));
    await click(approve());
    expect(onRelease).toHaveBeenCalledWith([expect.objectContaining({ id: "a" })]);
  });

  it("cannot approve with nothing ticked, and a note that stays alone is approvable again", async () => {
    const onRelease = vi.fn();
    const two = [held("a", "one"), held("b", "two")];
    await act(async () => {
      root!.render(<MemoReleaseCard card={card(two)} onRelease={onRelease} {...props} />);
    });
    await click(checks()[0]);
    await click(checks()[1]);
    expect(approve().disabled).toBe(true);
    expect(approve().textContent).toBe(t("memo.release_button_partial", { count: 0, total: 2 }));
    // The card is redrawn with one note left; its earlier untick no longer holds it back.
    await act(async () => {
      root!.render(<MemoReleaseCard card={card([two[0]])} onRelease={onRelease} {...props} />);
    });
    expect(approve().disabled).toBe(false);
    await click(approve());
    expect(onRelease).toHaveBeenCalledWith([expect.objectContaining({ id: "a" })]);
  });

  it("asks for the confirming press only for ticked notes", async () => {
    const onConfirm = vi.fn();
    const onRelease = vi.fn();
    const two = [held("a", "one"), held("b", "two")];
    const edited = new Set(["b"]);
    const awaiting = new Set(["b"]);
    const render = async () =>
      act(async () => {
        root!.render(<MemoReleaseCard card={card(two)} {...props} edited={edited} awaiting={awaiting} onRelease={onRelease} onConfirm={onConfirm} />);
      });
    await render();
    expect(approve().textContent).toBe(t("memo.release_confirm"));
    await click(checks()[1]);
    expect(approve().textContent).toBe(t("memo.release_button_partial", { count: 1, total: 2 }));
    await click(approve());
    expect(onRelease).toHaveBeenCalledWith([expect.objectContaining({ id: "a" })]);
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
