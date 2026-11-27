// Render tests for the memo section's auto-run side (ADR 0137 §10): which categories are
// marked, each message's state and origin, the release card (release and the stale refusal),
// the settings dialog (tenant switch off, validation) and the deliveries with "Run now".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { headMenuItem } from "./catMenuTest.ts";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
let memoListRejects = false;
let memoListCalls = 0;
let servedCats: MemoCategory[] = [];
let servedQueues: unknown = { tenantEnabled: true, queues: [] };
let queuesFail = false;
let servedDeliveries: unknown = [];

const memoRelease = vi.fn();
const memoQueuePut = vi.fn();
const memoQueueDelete = vi.fn();
const memoQueueFire = vi.fn();
const memoQueueFireOne = vi.fn();
const memoCategoryUpdateMock = vi.fn(async (_id: string, _patch: unknown) => ({}));
const memoQueueResume = vi.fn();
const memoUpdateMock = vi.fn(async (_id: string, _patch: unknown) => ({}));
const memoQueueDeliveryEnd = vi.fn();
const memoQueueDeliveryReturn = vi.fn();
const memoQueueDeliveries = vi.fn(async (_id: string, _openToo?: boolean, _since?: string) => servedDeliveries);
const memoStopChain = vi.fn(async (_id: string) => ({}));
// Answers the memo as the Control Plane does: runNext present only when set.
const memoSetRunNext = vi.fn(async (id: string, on: boolean): Promise<unknown> => (on ? { id, runNext: true } : { id }));
const memoQueueList = vi.fn(async () => {
  if (queuesFail) throw new Error("down");
  return servedQueues;
});

vi.mock("./api.ts", () => ({
  memoList: async () => {
    memoListCalls++;
    if (memoListRejects) throw new Error("connection reset");
    return servedMemos;
  },
  memoCategoryList: async () => servedCats,
  memoCreate: async () => ({}),
  memoUpdate: (id: string, patch: unknown) => memoUpdateMock(id, patch),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: (id: string, patch: unknown) => memoCategoryUpdateMock(id, patch),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: () => memoQueueList(),
  memoStopChain: (id: string) => memoStopChain(id),
  memoSetRunNext: (id: string, on: boolean) => memoSetRunNext(id, on),
  memoRelease: (items: unknown) => memoRelease(items),
  memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body),
  memoQueueDelete: (id: string) => memoQueueDelete(id),
  memoQueueFire: (id: string) => memoQueueFire(id),
  memoQueueFireOne: (id: string, memoId: string) => memoQueueFireOne(id, memoId),
  memoQueueResume: (id: string) => memoQueueResume(id),
  memoQueueDeliveries: (id: string, openToo?: boolean, since?: string) => (openToo === undefined ? memoQueueDeliveries(id) : memoQueueDeliveries(id, openToo, since)),
  memoQueueDeliveryEnd: (id: string, did: string) => memoQueueDeliveryEnd(id, did),
  memoQueueDeliveryReturn: (id: string, did: string) => memoQueueDeliveryReturn(id, did),
  memoTidyRuns: async () => ({ runs: [], usedToday: 0, dailyCap: 10 }),
  memoTidyUndo: async () => ({}),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));
const openSessionChat = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => openSessionChat(n), openSessionTerminal: vi.fn() }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t, setLocale } = await import("../../lib/i18n/index.ts");
const { fmtDateTime } = await import("../../lib/intl.ts");
const { errText } = await import("../../core/api/client.ts");
const { deliveryStateKey } = await import("./queues.ts");
const { resetShownNotes } = await import("./shownNotes.ts");
const { useQueueSettingsCache } = await import("./queueSettingsCache.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}
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
const aCat = (over: Partial<MemoCategory>): MemoCategory => ({
  id: "c1",
  repo: "",
  name: "inbox-q",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  ...over,
});
const aQueue = (over: Record<string, unknown> = {}) => ({
  categoryId: "c1",
  repo: "",
  category: "inbox-q",
  instruction: "triage",
  agentKind: "claude",
  model: "",
  effort: "",
  route: "r-keep",
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
  paused: false,
  pausedAt: "",
  pausedReason: "",
  draining: false,
  noReportAt: "",
  enabled: true,
  generation: 1,
  createdAt: "",
  updatedAt: "",
  ...over,
});
const held = (id: string, hash: string, over: Partial<Memo> = {}): Memo =>
  aMemo({ id, body: "body " + id, state: "held", provenance: "workspace", producer: "worker-1", contentHash: hash, ...over });

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
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
async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}
async function setValue(el: HTMLInputElement | HTMLTextAreaElement, v: string): Promise<void> {
  await act(async () => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const byLabel = (label: string) =>
  must(
    document.querySelector<HTMLElement>(`[aria-label="${label}"]`) ??
      (label === t("memo.autorun_settings") || label === t("memo.autorun_deliveries") ? headMenuItem({ label }) : null),
    `button ${label}`,
  );
const dialog = () => must(document.querySelector<HTMLElement>(".memo-q-modal"), "dialog");
const bump = async () => {
  const { useMemoStore } = await import("./store.ts");
  await act(async () => useMemoStore.getState().bump());
  await settle();
};

beforeEach(() => {
  resetShownNotes();
  useQueueSettingsCache.getState().reset();
  localStorage.clear();
  servedMemos = [];
  memoListRejects = false;
  memoUpdateMock.mockClear();
  servedCats = [aCat({ queue: true })];
  servedQueues = { tenantEnabled: true, queues: [aQueue()] };
  queuesFail = false;
  servedDeliveries = [];
  for (const m of [memoCategoryUpdateMock, memoRelease, memoQueuePut, memoQueueDelete, memoQueueFire, memoQueueFireOne, memoQueueResume, memoQueueDeliveries, memoQueueDeliveryEnd, memoQueueDeliveryReturn, memoQueueList, memoStopChain, memoSetRunNext, openSessionChat]) m.mockClear();
  memoRelease.mockResolvedValue({ released: [], stale: [] });
  memoQueuePut.mockResolvedValue(aQueue());
  memoQueueDelete.mockResolvedValue({ ok: true });
  memoQueueFire.mockResolvedValue({ id: "d1" });
  memoQueueFireOne.mockResolvedValue({ id: "d1" });
  memoQueueResume.mockResolvedValue({});
  memoQueueDeliveryEnd.mockResolvedValue({});
  memoQueueDeliveryReturn.mockResolvedValue({});
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  (await import("../sessions/store.ts")).useSessionsStore.setState({ sessions: [] });
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.querySelectorAll(".ui-modal-backdrop, .ui-modal").forEach((e) => e.remove());
});

describe("the list", () => {
  it("marks an auto-run category, on and off, and leaves a plain one alone", async () => {
    servedCats = [aCat({ queue: true }), aCat({ id: "c2", name: "plain", position: 1 })];
    servedMemos = [aMemo({ id: "p", category: "plain" })];
    await render();
    const groups = [...document.querySelectorAll<HTMLElement>(".memo-cat")];
    const badge = (n: string) =>
      groups.find((g) => g.querySelector(".memo-cat-name")?.textContent === n)!.querySelector(".memo-q-badge");
    expect(badge("inbox-q")?.textContent).toContain(t("memo.autorun_badge"));
    expect(badge("inbox-q")?.classList.contains("memo-q-badge--off")).toBe(false);
    expect(badge("plain")).toBeNull();

    servedQueues = { tenantEnabled: true, queues: [aQueue({ enabled: false })] };
    await bump();
    expect(badge("inbox-q")?.textContent).toContain(t("memo.autorun_badge_off"));
  });

  it("marks a category whose repository settings file differs when last read", async () => {
    const repo = () => document.querySelector(".memo-q-repo");
    const check = { commit: "c0ffeec0ffeec0ffeec0ffeec0ffeec0ffeec0ff", diffs: 2, checkedAt: "2026-11-23T00:00:00Z" };
    servedQueues = { tenantEnabled: true, queues: [aQueue({ repoCheck: { state: "differs", ...check } })] };
    await render();
    expect(repo()?.getAttribute("aria-label")).toBe(t("memo.repo_badge_differs"));
    servedQueues = { tenantEnabled: true, queues: [aQueue({ repoCheck: { state: "invalid", ...check, diffs: 0 } })] };
    await bump();
    expect(repo()?.getAttribute("aria-label")).toBe(t("memo.repo_badge_invalid"));
    servedQueues = { tenantEnabled: true, queues: [aQueue({ repoCheck: { state: "same", ...check, diffs: 0 } })] };
    await bump();
    expect(repo()).toBeNull();
  });

  it("shows each message's state and where it came from", async () => {
    servedMemos = [
      aMemo({ id: "a", state: "pending", provenance: "member" }),
      aMemo({ id: "b", state: "claimed", provenance: "mcp", producer: "pat_9" }),
      aMemo({ id: "c", state: "unconfirmed", provenance: "workspace", producer: "worker-2" }),
      aMemo({ id: "d", category: "inbox-q", state: "", provenance: "workspace", producer: "old" }),
    ];
    await render();
    const text = [...document.querySelectorAll(".memo-qmeta")].map((e) => e.textContent);
    expect(text).toHaveLength(3); // the plain note carries none
    expect(text[0]).toContain(t("memo.state_pending"));
    expect(text[0]).toContain(t("memo.origin_member"));
    expect(text[1]).toContain(t("memo.state_claimed"));
    expect(text[1]).toContain(t("memo.origin_mcp", { name: "pat_9" }));
    expect(text[2]).toContain(t("memo.state_unconfirmed"));
    expect(text[2]).toContain(t("memo.origin_session", { name: "worker-2" }));
  });

  it("folds started and replaced messages into one line that opens on click", async () => {
    servedMemos = [
      aMemo({ id: "a", body: "waiting-a", state: "pending", provenance: "member" }),
      aMemo({ id: "b", body: "started-b", state: "delivered", provenance: "member" }),
      aMemo({ id: "c", body: "replaced-c", state: "superseded", provenance: "member" }),
      aMemo({ id: "d", body: "look-d", state: "unconfirmed", provenance: "member" }),
    ];
    await render();
    const body = () => document.querySelector(".memo-cat-items")!.textContent ?? "";
    expect(body()).toContain("waiting-a");
    expect(body()).toContain("look-d"); // needs a decision, so it is never folded
    expect(body()).not.toContain("started-b");
    expect(body()).not.toContain("replaced-c");
    const fold = document.querySelector<HTMLButtonElement>(".memo-sent-fold")!;
    expect(fold.textContent).toContain(t("memo.sent_fold", { count: 2 }));
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    await act(async () => fold.click());
    expect(body()).toContain("started-b");
    expect(body()).toContain("replaced-c");
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!.click());
    expect(body()).not.toContain("started-b");
  });

  it("leaves a folded memo out of select-all, and opens the history below the waiting notes", async () => {
    servedMemos = [
      aMemo({ id: "b", body: "started-b", state: "delivered", provenance: "member", position: 0 }),
      aMemo({ id: "a", body: "waiting-a", state: "pending", provenance: "member", position: 1 }),
    ];
    await render();
    await act(async () => document.querySelector<HTMLInputElement>(".memo-cat-check")!.click());
    expect(document.querySelector(".memo-selbar-n")?.textContent).toContain(t("memo.selected_n", { count: 1 }));
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!.click());
    expect(document.querySelector(".memo-sent-fold")!.getAttribute("aria-expanded")).toBe("true");
    const bodies = [...document.querySelectorAll(".memo-cat-items .memo-body")].map((e) => e.textContent);
    expect(bodies.findIndex((x) => x?.includes("waiting-a"))).toBeLessThan(bodies.findIndex((x) => x?.includes("started-b")));
  });

  it("keeps the fold line right under the waiting notes when opened, with history newest first", async () => {
    servedMemos = [
      aMemo({ id: "o", body: "old-sent", state: "delivered", provenance: "member", position: 0, sentAt: "2026-09-01T00:00:00Z" }),
      aMemo({ id: "n", body: "new-sent", state: "delivered", provenance: "member", position: 1, sentAt: "2026-09-05T00:00:00Z" }),
      aMemo({ id: "a", body: "waiting-a", state: "pending", provenance: "member", position: 2 }),
    ];
    await render();
    const order = () =>
      [...document.querySelector(".memo-cat-items")!.children].map((e) =>
        e.classList.contains("memo-sent-fold") ? "FOLD" : (e.querySelector(".memo-body")?.textContent ?? ""),
      );
    const before = order();
    expect(before).toEqual([expect.stringContaining("waiting-a"), "FOLD"]);
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!.click());
    const after = order();
    expect(after[0]).toContain("waiting-a");
    expect(after[1]).toBe("FOLD");
    expect(after[2]).toContain("new-sent");
    expect(after[3]).toContain("old-sent");
  });

  it("still reorders waiting notes by drag, and ignores a drop on a history note", async () => {
    servedMemos = [
      aMemo({ id: "a", body: "waiting-a", state: "pending", provenance: "member", position: 0 }),
      aMemo({ id: "b", body: "waiting-b", state: "pending", provenance: "member", position: 1 }),
      aMemo({ id: "h", body: "started-h", state: "delivered", provenance: "member", position: 2 }),
    ];
    await render();
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!.click());
    const dt = { setData: () => {}, types: [], files: [], effectAllowed: "" };
    const fire = (el: Element, type: string) =>
      act(async () => {
        const ev = new Event(type, { bubbles: true, cancelable: true });
        Object.defineProperty(ev, "dataTransfer", { value: dt });
        el.dispatchEvent(ev);
      });
    const rows = () => [...document.querySelectorAll<HTMLElement>(".memo-cat-items .memo-row")];
    const bodies = () => rows().map((r) => r.querySelector(".memo-body")?.textContent ?? "");
    const rowOf = (txt: string) => rows().find((r) => r.textContent?.includes(txt))!;
    await fire(rowOf("waiting-a").querySelector(".memo-grip")!, "dragstart");
    await fire(rowOf("started-h"), "drop");
    await settle();
    expect(bodies().map((x) => x.slice(0, 9))).toEqual(["waiting-a", "waiting-b", "started-h"]);
    await fire(rowOf("waiting-b").querySelector(".memo-grip")!, "dragstart");
    await fire(rowOf("waiting-a"), "drop");
    await settle();
    expect(bodies().map((x) => x.slice(0, 9))).toEqual(["waiting-b", "waiting-a", "started-h"]);
    // The patches renumber the whole group in server order, history included, so the waiting order survives a refetch.
    expect(memoUpdateMock.mock.calls.map(([id, p]) => [id, (p as { position: number }).position])).toEqual([
      ["b", 0],
      ["a", 1],
      ["h", 2],
    ]);
  });

  it("sorts a history note with an unparsable time last instead of scrambling the rest", async () => {
    servedMemos = [
      aMemo({ id: "x", body: "bad-x", state: "delivered", provenance: "member", sentAt: "garbage", position: 0 }),
      aMemo({ id: "o", body: "old-o", state: "delivered", provenance: "member", sentAt: "2026-09-01T00:00:00Z", position: 1 }),
      aMemo({ id: "n", body: "new-n", state: "delivered", provenance: "member", sentAt: "2026-09-05T00:00:00Z", position: 2 }),
    ];
    await render();
    await act(async () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!.click());
    const bodies = [...document.querySelectorAll(".memo-cat-items .memo-body")].map((e) => (e.textContent ?? "").slice(0, 5));
    expect(bodies).toEqual(["new-n", "old-o", "bad-x"]);
  });

  it("publishes the measured category head height on the category for the sticky fold line", async () => {
    const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get() {
        return (this as HTMLElement).classList.contains("memo-cat-head") ? 44 : 0;
      },
    });
    try {
      servedMemos = [aMemo({ id: "b", state: "delivered", provenance: "member" })];
      await render();
      const cat = document.querySelector<HTMLElement>(".memo-cat")!;
      expect(cat.style.getPropertyValue("--memo-cat-head-h")).toBe("44px");
      expect(cat.contains(document.querySelector(".memo-sent-fold"))).toBe(true);
    } finally {
      if (desc) Object.defineProperty(HTMLElement.prototype, "offsetHeight", desc);
    }
  });

  it("clears a folded memo's selection when select-all is turned off with the fold closed", async () => {
    servedMemos = [
      aMemo({ id: "b", body: "started-b", state: "delivered", provenance: "member", position: 0 }),
      aMemo({ id: "a", body: "waiting-a", state: "pending", provenance: "member", position: 1 }),
    ];
    await render();
    const fold = () => document.querySelector<HTMLButtonElement>(".memo-sent-fold")!;
    await act(async () => fold().click());
    await act(async () => document.querySelector<HTMLInputElement>(".memo-cat-check")!.click());
    expect(document.querySelector(".memo-selbar-n")?.textContent).toContain(t("memo.selected_n", { count: 2 }));
    await act(async () => fold().click());
    await act(async () => document.querySelector<HTMLInputElement>(".memo-cat-check")!.click());
    expect(document.querySelector(".memo-selbar")).toBeNull();
  });

  it("does not fold a plain category's started memos", async () => {
    servedCats = [aCat({ queue: true }), aCat({ id: "c2", name: "plain", position: 1 })];
    servedMemos = [aMemo({ id: "b", category: "plain", state: "delivered", provenance: "member" })];
    await render();
    expect(document.querySelector(".memo-sent-fold")).toBeNull();
  });

  it("shows no fold line when nothing was started", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    expect(document.querySelector(".memo-sent-fold")).toBeNull();
  });

  it("links a live producer session to its view and leaves other producers as plain text", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: [{ name: "worker-2" }, { name: "worker-1" }] as never });
    servedMemos = [
      aMemo({ id: "b", state: "claimed", provenance: "mcp", producer: "worker-2" }),
      aMemo({ id: "c", state: "unconfirmed", provenance: "workspace", producer: "worker-2" }),
      aMemo({ id: "d", state: "unconfirmed", provenance: "workspace", producer: "gone" }),
      held("h", "h1"),
    ];
    await render();
    const origins = [...document.querySelectorAll(".memo-qorigin")];
    expect(origins[0]!.querySelector("button")).toBeNull(); // an /mcp connection is not a session
    const link = must(origins[1]!.querySelector("button"), "producer link");
    expect(origins[1]!.textContent).toBe(t("memo.origin_session", { name: "worker-2" }));
    await click(link);
    expect(openSessionChat).toHaveBeenCalledWith("worker-2");
    expect(document.querySelector(".memo-row.exp")).toBeNull(); // the click did not expand the row
    expect(origins[2]!.querySelector("button")).toBeNull(); // deleted session
    const title = must(document.querySelector(".memo-release-title button"), "card link");
    await click(title);
    expect(openSessionChat).toHaveBeenCalledWith("worker-1");
  });

  it("does not link a held card whose provenance is not a session", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: [{ name: "x" }] as never });
    servedMemos = [held("m1", "h1", { provenance: "member", producer: "x" })];
    await render();
    expect(document.querySelector(".memo-release-title")).not.toBeNull();
    expect(document.querySelector(".memo-release-title button")).toBeNull();
  });

  it("survives a JSON null where the settings expect an array, and a failed settings read", async () => {
    servedMemos = [aMemo({ id: "a" })];
    servedQueues = { tenantEnabled: true, queues: null };
    await render();
    expect(document.querySelectorAll(".memo-row")).toHaveLength(1);

    queuesFail = true;
    servedMemos = [aMemo({ id: "a" }), aMemo({ id: "b" })];
    await bump();
    expect(document.querySelectorAll(".memo-row")).toHaveLength(2);
  });

  it("reads the settings with the memo poll and deliveries only for an opened queue", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    expect(memoQueueList).toHaveBeenCalledTimes(1);
    expect(memoQueueDeliveries).not.toHaveBeenCalled();
  });
});

describe("the release card", () => {
  it("lists the held messages of a session and releases them with the hash each row showed", async () => {
    servedMemos = [held("m1", "h1"), held("m2", "h2"), aMemo({ id: "m3", state: "pending", provenance: "member" })];
    await render();
    const card = must(document.querySelector<HTMLElement>(".memo-release"), "card");
    expect(card.textContent).toContain("worker-1");
    expect(card.querySelectorAll(".memo-release-item")).toHaveLength(2);
    await click(must(card.querySelector("button"), "release button"));
    expect(memoRelease).toHaveBeenCalledWith([
      { id: "m1", contentHash: "h1" },
      { id: "m2", contentHash: "h2" },
    ]);
  });

  it("on a stale refusal re-reads, marks what changed, and releases only what the new card shows", async () => {
    servedMemos = [held("m1", "h1"), held("m2", "h2")];
    await render();
    memoRelease.mockResolvedValueOnce({ error: { code: "memo_release_stale", stale: ["m2"] } });
    // The session edited m2 meanwhile: the re-read serves a new hash for it.
    servedMemos = [held("m1", "h1"), held("m2", "h2-new", { body: "edited" })];
    await click(must(document.querySelector(".memo-release button"), "release button"));
    const card = must(document.querySelector<HTMLElement>(".memo-release"), "card again");
    expect(card.classList.contains("changed")).toBe(true);
    expect(card.querySelectorAll(".memo-release-changed")).toHaveLength(1);
    expect(card.textContent).toContain("edited");
    // The tag sits inside the body so the text runs the full card width (#4901).
    expect(card.querySelectorAll(".memo-release-body .memo-release-changed")).toHaveLength(1);
    // The text under the button is not the one first shown, so the first press only confirms.
    await click(must(card.querySelector("button"), "confirm button"));
    expect(memoRelease).toHaveBeenCalledTimes(1);
    await click(must(document.querySelector(".memo-release button"), "release button"));
    expect(memoRelease).toHaveBeenLastCalledWith([
      { id: "m1", contentHash: "h1" },
      { id: "m2", contentHash: "h2-new" },
    ]);
    expect(memoRelease).toHaveBeenCalledTimes(2);
  });

  it("shows the whole body, so a change past any cut is seen before it can be approved", async () => {
    const long = "x".repeat(600);
    servedMemos = [held("m1", "h1", { body: long + " TAIL-ONE" })];
    await render();
    const text = () => document.querySelector(".memo-release-text")?.textContent ?? "";
    expect(text()).toBe(long + " TAIL-ONE");
    memoRelease.mockResolvedValueOnce({ error: { code: "memo_release_stale", stale: ["m1"] } });
    servedMemos = [held("m1", "h2", { body: long + " TAIL-TWO" })];
    await click(must(document.querySelector(".memo-release button"), "release button"));
    expect(text()).toBe(long + " TAIL-TWO");
  });

  it("lists the attachments, so a changed attachment shows on the redrawn card", async () => {
    servedMemos = [held("m1", "h1", { attachments: [{ path: "/p/a.png", name: "a.png" }] })];
    await render();
    const names = () => [...document.querySelectorAll(".memo-release-att")].map((e) => e.getAttribute("title"));
    expect(names()).toEqual(["a.png"]);
    memoRelease.mockResolvedValueOnce({ error: { code: "memo_release_stale", stale: ["m1"] } });
    servedMemos = [held("m1", "h2", { attachments: [{ path: "/p/b.png", name: "b.png" }, { path: "/p/c.png", name: "c.png" }] })];
    await click(must(document.querySelector(".memo-release button"), "release button"));
    expect(names()).toEqual(["b.png", "c.png"]);
  });

  it("makes one card per producer", async () => {
    servedMemos = [held("m1", "h1"), held("m2", "h2", { producer: "worker-2" }), held("m3", "h3", { provenance: "mcp", producer: "pat_1" })];
    await render();
    expect(document.querySelectorAll(".memo-release")).toHaveLength(3);
  });
});

describe("the settings dialog", () => {
  const openSettings = async () => click(byLabel(t("memo.autorun_settings")));

  // The reason is on the item's second line; each test asserts it.
  const gearByReason = (_reason: string) => headMenuItem({ label: t("memo.autorun_settings") }) as HTMLButtonElement;

  it("marks the gear unavailable with the reason while the tenant blocks new setups", async () => {
    servedQueues = { tenantEnabled: false, queues: [] };
    servedCats = [aCat({})];
    await render();
    const gear = gearByReason(t("memo.q_tenant_off"));
    // Not natively disabled: it must stay focusable so the reason reaches keyboard users.
    expect(gear.disabled).toBe(false);
    expect(gear.getAttribute("aria-disabled")).toBe("true");
    expect(gear.textContent).toContain(t("memo.q_tenant_off"));
    const head = must(document.querySelector(".memo-cat-head"), "head");
    const expanded = head.querySelector("[aria-expanded]")?.getAttribute("aria-expanded");
    await click(gear);
    expect(document.querySelector("[role=dialog]")).toBeNull();
    // The reason is on the item itself, so a press says nothing more.
    expect(document.querySelector(".ui-toast-msg")).toBeNull();
    expect(head.querySelector("[aria-expanded]")?.getAttribute("aria-expanded")).toBe(expanded);
  });

  it("keeps the gear available when the tenant allows auto-run", async () => {
    servedQueues = { tenantEnabled: true, queues: [] };
    servedCats = [aCat({})];
    await render();
    const gear = byLabel(t("memo.autorun_settings"));
    expect(gear.getAttribute("aria-disabled")).toBeNull();
    expect(gear.textContent).toContain(t("memo.autorun_settings"));
  });

  it("does not open the dialog, nor call it a tenant block, while the settings are unread", async () => {
    queuesFail = true;
    servedCats = [aCat({ queue: true })]; // an existing queue whose settings could not be read
    await render();
    const gear = gearByReason(t("memo.q_unread"));
    expect(gear.getAttribute("aria-disabled")).toBe("true");
    await click(gear);
    // Opening it would edit a draft built from defaults and a save would overwrite the real setting.
    expect(document.querySelector(".memo-q-modal")).toBeNull();
    expect(document.querySelector(".ui-toast-msg")).toBeNull();
    expect(memoQueuePut).not.toHaveBeenCalled();
  });

  it("opens the saved setting once a later read succeeds", async () => {
    queuesFail = true;
    servedCats = [aCat({ queue: true })];
    await render();
    expect(headMenuItem({ label: t("memo.autorun_settings") }).textContent).toContain(t("memo.q_unread"));
    queuesFail = false;
    servedQueues = { tenantEnabled: true, queues: [aQueue({ instruction: "saved text" })] };
    await bump();
    await click(byLabel(t("memo.autorun_settings")));
    expect(must(dialog().querySelector<HTMLTextAreaElement>("textarea"), "instruction").value).toBe("saved text");
  });

  it("does not carry a blocked tenant's setting over to another tenant whose read failed", async () => {
    const { useTenantStore } = await import("../../core/store/tenant.ts");
    const switchTo = async (tenant: string) => {
      await act(async () => useTenantStore.setState({ tenant }));
      await settle();
    };
    await switchTo("a");
    servedQueues = { tenantEnabled: false, queues: [] };
    servedCats = [aCat({})];
    await render();
    expect(headMenuItem({ label: t("memo.autorun_settings") }).textContent).toContain(t("memo.q_tenant_off"));
    queuesFail = true;
    await switchTo("b");
    expect(headMenuItem({ label: t("memo.autorun_settings") }).textContent).toContain(t("memo.q_unread"));
    await switchTo("");
  });

  it("still lets an existing setting be removed while the switch is off", async () => {
    servedQueues = { tenantEnabled: false, queues: [aQueue()] };
    await render();
    await openSettings();
    expect(dialog().querySelector(".memo-q-notice")?.textContent).toBe(t("memo.q_tenant_off_existing"));
    const remove = must(
      [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("memo.q_remove")),
      "remove button",
    );
    expect(remove.disabled).toBe(false);
  });

  it("names a missing instruction next to the field and does not call the server", async () => {
    servedQueues = { tenantEnabled: true, queues: [] };
    servedCats = [aCat({})];
    await render();
    await openSettings();
    await click(must(dialog().querySelector<HTMLInputElement>("#memo-q-instruction"), "x")); // focus only
    const enable = must(
      [...dialog().querySelectorAll("label")].find((l) => l.textContent?.includes(t("memo.q_enabled")))?.querySelector<HTMLInputElement>("input[type=checkbox]"),
      "enabled switch",
    );
    await click(enable);
    await click(must(dialog().querySelector("button[type=submit]"), "save"));
    expect(dialog().querySelector("[data-invalid]")?.textContent).toBe(t("memo.q_err_instruction"));
    expect(memoQueuePut).not.toHaveBeenCalled();
  });

  it("names a number out of range", async () => {
    await render();
    await openSettings();
    await setValue(must(dialog().querySelector<HTMLInputElement>("#memo-q-max-active"), "max"), "9");
    await click(must(dialog().querySelector("button[type=submit]"), "save"));
    expect(dialog().querySelector("[data-invalid]")?.textContent).toBe(t("memo.q_err_max_active", { max: 6 }));
    expect(memoQueuePut).not.toHaveBeenCalled();
  });

  it("saves the whole row, sending the route it does not edit back as it was", async () => {
    await render();
    await openSettings();
    await setValue(must(dialog().querySelector<HTMLInputElement>("#memo-q-batch"), "batch"), "3");
    await click(must(dialog().querySelector("button[type=submit]"), "save"));
    expect(memoQueuePut).toHaveBeenCalledWith("c1", {
      instruction: "triage",
      agentKind: "claude",
      model: "",
      effort: "",
      route: "r-keep",
      spendCapUsd: 0,
      worktree: true,
      acceptOwnSessions: false,
      continuationMax: 4,
      tidy: false,
      tidyDestinations: [],
      maxActive: 2,
      batch: 3,
      maxTaskHours: 12,
      openCron: "",
      openMinutes: 0,
      tz: "",
      openAfter: 0,
      maxFiresPerDay: 20,
      enabled: true,
    });
  });

  it("shows the Control Plane's wording when the save is refused", async () => {
    memoQueuePut.mockResolvedValue({ error: { code: "memo_queues_disabled", message: "off" } });
    await render();
    await openSettings();
    await click(must(dialog().querySelector("button[type=submit]"), "save"));
    expect(document.body.textContent).toContain(t("err.memo_queues_disabled"));
    expect(dialog()).toBeTruthy(); // stays open
  });
});

describe("deliveries and Run now", () => {
  const openDeliveries = async () => click(byLabel(t("memo.autorun_deliveries")));

  it("reads the deliveries of the opened queue and links each to its session", async () => {
    servedDeliveries = [
      { id: "d1", state: "open", outcome: "", session: "run-1", messageIds: ["a", "b"], createdAt: "t1", sentAt: "", closedAt: "" },
      { id: "d2", state: "closed", outcome: "failed", session: "", messageIds: null, createdAt: "t0", sentAt: "", closedAt: "" },
    ];
    await render();
    await openDeliveries();
    expect(memoQueueDeliveries).toHaveBeenCalledWith("c1");
    const rows = dialog().querySelectorAll(".memo-d-row");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain(t("memo.d_state_running"));
    expect(rows[1].textContent).toContain(t("memo.d_state_failed"));
    await click(must(rows[0].querySelector("button"), "session link"));
    expect(openSessionChat).toHaveBeenCalledWith("run-1");
  });

  it("fires the queue now from the category head, and the run history has no Run now", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await openDeliveries();
    expect([...dialog().querySelectorAll<HTMLButtonElement>("button")].some((b) => b.textContent?.includes(t("memo.d_fire")))).toBe(false);
    expect(dialog().textContent).toContain(t("memo.d_waiting", { count: 1 }));
  });

  describe("Run now on the category head", () => {
    const fireBtn = () => headMenuItem({ cls: "memo-cat-run" });
    it("starts a run when a waiting memo can start and nothing blocks it", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      await render();
      expect(fireBtn().getAttribute("aria-disabled")).toBeNull();
      expect(fireBtn().textContent).toContain(t("memo.cat_menu_run"));
      await click(fireBtn());
      expect(memoQueueFire).toHaveBeenCalledWith("c1");
      expect(document.body.textContent).toContain(t("memo.d_fire_started"));
    });

    const blocked = async (fireBlock: unknown, reason: string) => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedQueues = { tenantEnabled: true, queues: [aQueue({ fireBlock })] };
      await render();
      expect(fireBtn().getAttribute("aria-disabled")).toBe("true");
      expect(fireBtn().textContent).toContain(reason);
      await click(fireBtn());
      expect(memoQueueFire).not.toHaveBeenCalled();
    };
    it("is disabled with nothing waiting, as the queue list says", async () => {
      servedQueues = { tenantEnabled: true, queues: [aQueue({ fireBlock: { reason: "nothing_waiting" } })] };
      await render();
      expect(fireBtn().getAttribute("aria-disabled")).toBe("true");
      expect(fireBtn().textContent).toContain(t("memo.d_fire_none"));
    });
    it("follows the queue list, not the memo list: an older Control Plane or a lagging memo read leaves it enabled", async () => {
      servedMemos = []; // the memo read has not seen the new note yet
      await render();
      expect(fireBtn().getAttribute("aria-disabled")).toBeNull();
    });
    it("keeps the queue list when the memo or category read fails", async () => {
      servedMemos = { error: { code: "x" } } as never;
      servedQueues = { tenantEnabled: true, queues: [aQueue({ fireBlock: { reason: "nothing_waiting" } })] };
      await render();
      expect(useQueueSettingsCache.getState().queues[0]?.fireBlock?.reason).toBe("nothing_waiting");
    });
    it("keeps the queue list when the memo read itself fails", async () => {
      memoListRejects = true;
      servedQueues = { tenantEnabled: true, queues: [aQueue({ fireBlock: { reason: "no_place", detail: { by: "category" } } })] };
      await render();
      expect(useQueueSettingsCache.getState().queues[0]?.fireBlock?.reason).toBe("no_place");
    });
    it("does not fold the category when a disabled button is pressed", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedQueues = { tenantEnabled: true, queues: [aQueue({ fireBlock: { reason: "prerequisites", detail: { blocked: 1 } } })] };
      await render();
      const caret = must(document.querySelector<HTMLElement>(".memo-cat-head .memo-cat-caret"), "category caret");
      const before = caret.getAttribute("aria-expanded");
      expect(before).toBe("true");
      await click(fireBtn());
      expect(caret.getAttribute("aria-expanded")).toBe(before);
      expect(memoQueueFire).not.toHaveBeenCalled();
    });
    it("is disabled while every waiting memo waits for another memo, and says so", async () => {
      await blocked({ reason: "prerequisites", detail: { blocked: 5 } }, t("memo.waiting_prerequisites", { count: 5 }));
    });
    it("is disabled while every waiting memo was handed back", async () => {
      await blocked({ reason: "deferred", detail: { deferred: 2 } }, t("memo.waiting_deferred", { count: 2 }));
    });
    it("is disabled while the category's slots are full, and for the member's limit", async () => {
      await blocked({ reason: "no_place", detail: { by: "category" } }, t("memo.waiting_no_place"));
    });
    it("is disabled while the member's own limit is reached", async () => {
      await blocked({ reason: "no_place", detail: { by: "member" } }, t("memo.waiting_no_place_member"));
    });
    it("stays enabled when the queue is paused or switched off: a press ignores both", async () => {
      servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true, enabled: false })] };
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      await render();
      expect(fireBtn().getAttribute("aria-disabled")).toBeNull();
    });
  });

  describe("a run that handed its memo back (ADR 0137, addendum 2026-11-19, part B)", () => {
    const base = { session: "run-1", messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "t2" };
    const confirmBtns = () => [...document.querySelectorAll<HTMLElement>(".ui-confirm-actions button")];
    const returnBtn = (row: Element) => [...row.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("memo.d_return"));

    it("shows the hand-back with its reason and time, and what a dropped one was dropped for", async () => {
      servedDeliveries = [
        { ...base, id: "d1", state: "closed", outcome: "deferred", defer: { reason: "waiting for #12", notBefore: "2026-11-20T12:00:00Z", at: "x", result: "applied" } },
        { ...base, id: "d2", state: "closed", outcome: "clean", defer: { reason: "later", notBefore: "2026-11-20T12:00:00Z", at: "x", result: "pr" } },
        { ...base, id: "d3", state: "closed", outcome: "clean", returnedAt: "2026-11-20T10:00:00Z" },
      ];
      await render();
      await openDeliveries();
      const rows = dialog().querySelectorAll(".memo-d-row");
      expect(rows[0].textContent).toContain(t("memo.d_state_deferred"));
      expect(rows[0].textContent).toContain(t("memo.d_defer_applied"));
      expect(rows[0].textContent).toContain("waiting for #12");
      expect(rows[1].textContent).toContain(t("memo.d_defer_dropped_pr"));
      // A run already returned offers no second press.
      expect(returnBtn(rows[2])).toBeUndefined();
      expect(returnBtn(rows[1])).toBeTruthy();
    });

    it("asks first, warns about an open pull request, and returns the run's memos on confirm", async () => {
      servedDeliveries = [{ ...base, id: "d1", state: "closed", outcome: "clean" }];
      const { useSessionsStore } = await import("../sessions/store.ts");
      useSessionsStore.setState({
        sessions: [{ name: "run-1", pr: { number: 12, state: "open", url: "https://github.com/o/r/pull/12" } } as never],
      });
      await render();
      await openDeliveries();
      const row = must(dialog().querySelector(".memo-d-row"), "row");
      await click(must(returnBtn(row), "return"));
      expect(memoQueueDeliveryReturn).not.toHaveBeenCalled();
      expect(document.querySelector(".ui-confirm")?.textContent).toContain(t("memo.d_return_body_pr", { name: "run-1", prs: "#12" }));
      await click(must(confirmBtns().find((b) => b.textContent === t("memo.d_return_confirm")), "confirm"));
      expect(memoQueueDeliveryReturn).toHaveBeenCalledWith("c1", "d1");
      useSessionsStore.setState({ sessions: [] });
    });

    it("says so when the Control Plane refuses", async () => {
      servedDeliveries = [{ ...base, id: "d1", state: "closed", outcome: "clean" }];
      memoQueueDeliveryReturn.mockResolvedValueOnce({ error: { code: "memo_ref_taken", message: "message m2 already holds o/r#12" } });
      await render();
      await openDeliveries();
      await click(must(returnBtn(must(dialog().querySelector(".memo-d-row"), "row")), "return"));
      expect(document.querySelector(".ui-confirm")?.textContent).toContain(t("memo.d_return_body"));
      await click(must(confirmBtns().find((b) => b.textContent === t("memo.d_return_confirm")), "confirm"));
      expect(document.body.textContent).toContain("message m2 already holds o/r#12");
    });
  });

  describe("run history names its work item (#4877)", () => {
    it("shows the work item keys and the start of the note on the row, with the return button before them", async () => {
      servedMemos = [
        aMemo({ id: "a", state: "delivered", body: "Fix the login redirect\nmore text", reference: { provider: "github", key: "o/r#12", url: "" } }),
      ];
      servedDeliveries = [{ id: "d1", state: "closed", outcome: "clean", session: "run-1", messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "t2" }];
      await render();
      await openDeliveries();
      const row = must(dialog().querySelector(".memo-d-row"), "row");
      const work = must(row.querySelector<HTMLElement>(".memo-d-work"), "work item");
      expect(work.textContent).toContain("o/r#12");
      expect(work.textContent).toContain("Fix the login redirect");
      expect(work.textContent).not.toContain("more text");
      expect(work.title).toContain("o/r#12");
      const btn = [...row.querySelectorAll("button")].find((b) => b.textContent === t("memo.d_return"));
      expect(btn).toBeTruthy();
      expect(btn!.compareDocumentPosition(work) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });
    it("shows nothing for a run whose notes left the list", async () => {
      servedMemos = [];
      servedDeliveries = [{ id: "d1", state: "closed", outcome: "clean", session: "run-1", messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "t2" }];
      await render();
      await openDeliveries();
      expect(dialog().querySelector(".memo-d-work")).toBeNull();
    });
  });

  describe("when Run now is refused", () => {
    const pressFire = async () => click(headMenuItem({ cls: "memo-cat-run" }));
    const noSlot = { error: { code: "memo_queue_no_slot", message: "every slot of this queue is taken (2 of 2)" } };
    const run = (id: string, state: string, session: string, heldBy = "", heldWakers: unknown[] = []) => ({
      id, state, outcome: state === "closed" ? "clean" : "", session, messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "", heldBy, heldWakers,
    });

    it("opens the history, which names the runs holding the slots and why", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedDeliveries = [
        run("d1", "open", "run-1", "person"),
        run("d2", "open", "run-2"),
        run("d4", "claimed", ""),
        run("d3", "closed", "run-0"),
        run("d5", "open", "run-5", "wakers", [{ id: "w_7", source: "github" }]),
      ];
      memoQueueFire.mockResolvedValueOnce(noSlot);
      await render();
      expect(document.querySelector(".memo-q-modal")).toBeNull();
      await pressFire();
      const note = must(dialog().querySelector<HTMLElement>(".memo-d-slots-full"), "slots-full notice");
      expect(note.textContent).toContain(t("memo.d_slots_full"));
      expect(note.textContent).toContain(t("memo.d_slots_holder", { name: "run-1", why: t("memo.d_held_person") }));
      expect(note.textContent).toContain(t("memo.d_slots_holder", { name: "run-2", why: t("memo.d_slots_no_reason") }));
      expect(note.textContent).toContain(t("memo.d_slots_holder", { name: t("memo.d_no_session"), why: t("memo.d_state_starting") }));
      expect(note.textContent).toContain(t("memo.d_slots_holder", { name: "run-5", why: t("memo.d_held_wakers_named", { names: "w_7 (github)" }) }));
      expect(note.textContent).not.toContain("run-0");
    });

    it("says the runs have finished when none holds a slot any more", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedDeliveries = [run("d3", "closed", "run-0")];
      memoQueueFire.mockResolvedValueOnce(noSlot);
      await render();
      await pressFire();
      const note = must(dialog().querySelector<HTMLElement>(".memo-d-slots-full"), "notice");
      expect(note.textContent).toBe(t("memo.d_slots_cleared"));
    });

    it("says the history could not be read instead of an empty list", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedDeliveries = { error: { code: "x" } };
      memoQueueFire.mockResolvedValueOnce(noSlot);
      await render();
      await pressFire();
      const note = must(dialog().querySelector<HTMLElement>(".memo-d-slots-full"), "notice");
      expect(note.textContent).toBe(t("memo.d_load_failed"));
      expect(note.querySelector("ul")).toBeNull();
    });

    it("reads the holders again when the history is already open", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedDeliveries = [];
      memoQueueFire.mockResolvedValueOnce(noSlot);
      await render();
      await click(byLabel(t("memo.autorun_deliveries")));
      expect(dialog().querySelector(".memo-d-slots-full")).toBeNull();
      servedDeliveries = [run("d1", "open", "run-1", "person")]; // took the slot after the history was read
      await click(headMenuItem({ cls: "memo-cat-run" }));
      expect(dialog().querySelector(".memo-d-slots-full")?.textContent).toContain(t("memo.d_slots_holder", { name: "run-1", why: t("memo.d_held_person") }));
    });

    it("does not say the slots are free when the re-read after the refusal fails", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      servedDeliveries = []; // the open history read an empty list
      memoQueueFire.mockResolvedValueOnce(noSlot);
      await render();
      await click(byLabel(t("memo.autorun_deliveries")));
      servedDeliveries = { error: { code: "x" } };
      await click(headMenuItem({ cls: "memo-cat-run" }));
      const note = must(dialog().querySelector<HTMLElement>(".memo-d-slots-full"), "notice");
      expect(note.textContent).toBe(t("memo.d_load_failed"));
    });

    it("only toasts for another refusal and opens nothing", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      memoQueueFire.mockResolvedValueOnce({ error: { code: "memo_queue_member_limit", message: "x" } });
      await render();
      await pressFire();
      expect(document.querySelector(".memo-q-modal")).toBeNull();
    });

    it("opens the history without the slot notice when the member opens it themselves", async () => {
      servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
      await render();
      await click(byLabel(t("memo.autorun_deliveries")));
      expect(dialog().querySelector(".memo-d-slots-full")).toBeNull();
    });
  });

  it("shows a read that is slower than the poll interval, then a newer one, and never an older over a newer", async () => {
    const slow = () => {
      let resolve!: (v: unknown) => void;
      const p = new Promise<unknown>((r) => (resolve = r));
      return { p, resolve };
    };
    const one = (id: string, session: string) => [{ id, state: "closed", outcome: "clean", session, messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "" }];
    const first = slow();
    const second = slow();
    memoQueueDeliveries.mockImplementationOnce(() => first.p as Promise<never>).mockImplementationOnce(() => second.p as Promise<never>);
    await render();
    await click(byLabel(t("memo.autorun_deliveries")));
    // A second read starts while the first is still out (the poll, or Refresh).
    await click(must([...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("memo.d_refresh")), "refresh"));
    await act(async () => first.resolve(one("d1", "run-first")));
    await settle();
    expect(dialog().textContent).toContain("run-first");
    await act(async () => second.resolve(one("d2", "run-second")));
    await settle();
    expect(dialog().textContent).toContain("run-second");
    expect(dialog().textContent).not.toContain("run-first");
  });

  it("says so when the history cannot be read", async () => {
    servedDeliveries = { error: { code: "x" } };
    await render();
    await openDeliveries();
    expect(dialog().textContent).toContain(t("memo.d_load_failed"));
  });
});

describe("every delivery state and outcome has a member's word", () => {
  const states = ["claimed", "sending", "open"];
  const outcomes = ["clean", "failed", "aborted", "unknown", "not_sent", "ended"];
  it("maps each open state and each closed outcome to a catalogue key whose text is not the identifier", () => {
    const all = [...states.map((state) => ({ state, outcome: "" })), ...outcomes.map((outcome) => ({ state: "closed", outcome }))];
    for (const d of all) {
      const k = deliveryStateKey(d);
      expect(k, JSON.stringify(d)).not.toBeNull();
      const text = t(k!);
      // "Failed" is a word a member uses; the raw identifiers are not (claimed, sending, not_sent, ...).
      for (const id of ["claimed", "sending", "open", "clean", "aborted", "unknown", "not_sent"]) expect(text.toLowerCase()).not.toBe(id);
      expect(text).not.toMatch(/queue|slot|キュー|スロット/i);
    }
  });
});

describe("a session that auto-run retired", () => {
  it("tells the member to start the session first, in the catalogue line alone", async () => {
    const { errDetail } = await import("../../core/api/client.ts");
    const error = { code: "session_retired", message: "session_retired: this session was retired; resume it from the Console first" };
    expect(errDetail(error)).toBe(t("err.session_retired"));
    expect(t("err.session_retired")).toMatch(/Start|開始/);
    // The same line is shown for a session an overdue run stopped while its run is still open, so
    // it must not say the run finished.
    expect(t("err.session_retired")).not.toMatch(/finished|完了/);
    const en = (await import("../../lib/i18n/locales/en/errors.ts")).errors as Record<string, string>;
    const ja = (await import("../../lib/i18n/locales/ja/errors.ts")).errors as Record<string, string>;
    expect(en["err.session_retired"]).toBe("Auto-run stopped this session. Start the session first, then send again.");
    expect(ja["err.session_retired"]).toBe("このセッションは自動実行によって停止されています。先にセッションを開始してから、もう一度送ってください。");
  });
});

describe("the memo auto-run error codes have a text of their own", () => {
  const codes = [
    "memo_locked", "memo_sending", "memo_in_queue", "memo_queue_category", "memo_inbox_in_queue", "memo_release_stale",
    "memo_too_large", "memo_envelope", "memo_rate_limited", "memo_queue_no_instruction", "memo_queue_bad_settings",
    "memo_queues_disabled", "memo_queue_nothing_pending", "memo_queue_no_slot", "memo_queue_member_limit", "memo_queue_bad_repo",
    "session_retired",
  ];
  it("answers the catalogue line, never the server's message", () => {
    for (const code of codes) expect(errText({ code, message: "SERVER-MESSAGE" }), code).not.toBe("SERVER-MESSAGE");
  });
});

// ---- step 2: when it runs, pause, why a run is open, the changed-note confirmation -----------

const openSettingsDialog = async () => click(byLabel(t("memo.autorun_settings")));
const radio = (label: string) =>
  must(
    [...dialog().querySelectorAll<HTMLLabelElement>("label.memo-q-when-opt")].find((l) => l.textContent?.includes(label))?.querySelector("input"),
    `radio ${label}`,
  );
const field = (id: string) => must(dialog().querySelector<HTMLInputElement>("#" + id), id);
const save = async () => click(must(dialog().querySelector("button[type=submit]"), "save"));
const bodySent = () => memoQueuePut.mock.calls[0][1] as Record<string, unknown>;

describe("when a category runs", () => {
  it("defaults to any time and sends the off values of the two other ways", async () => {
    await render();
    await openSettingsDialog();
    expect(radio(t("memo.q_when_always")).checked).toBe(true);
    expect(dialog().querySelector("#memo-q-open-cron")).toBeNull();
    await save();
    expect(bodySent()).toMatchObject({ openCron: "", openMinutes: 0, tz: "", openAfter: 0, maxFiresPerDay: 20, maxTaskHours: 12 });
  });

  it("a time window asks for the cron field, a length and a zone that starts as the browser's", async () => {
    const zone = vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({ timeZone: "Asia/Tokyo" } as Intl.ResolvedDateTimeFormatOptions);
    // browserTimeZone() caches for a second; a read that landed just before the spy would
    // answer the old zone (seen in CI), so step the clock past the cache.
    const realNow = Date.now();
    const clock = vi.spyOn(Date, "now").mockReturnValue(realNow + 60_000);
    try {
      await render();
      await openSettingsDialog();
      await click(radio(t("memo.q_when_window")));
      expect(field("memo-q-tz").value).toBe("Asia/Tokyo");
      expect(dialog().querySelector("#memo-q-open-after")).toBeNull();
      // Nothing entered yet: the cron field is named, the server is not called.
      await save();
      expect(dialog().querySelector("[data-invalid]")?.textContent).toBe(t("memo.q_err_cron"));
      expect(memoQueuePut).not.toHaveBeenCalled();
      await setValue(field("memo-q-open-cron"), "0 9 * * 1-5");
      await setValue(field("memo-q-open-minutes"), "2000");
      await save();
      expect(dialog().textContent).toContain(t("memo.q_err_window", { max: 1440 }));
      await setValue(field("memo-q-open-minutes"), "120");
      await setValue(field("memo-q-tz"), "Nowhere/Land");
      await save();
      expect(dialog().textContent).toContain(t("memo.q_err_tz"));
      expect(memoQueuePut).not.toHaveBeenCalled();
      await setValue(field("memo-q-tz"), "Asia/Tokyo");
      await save();
      expect(bodySent()).toMatchObject({ openCron: "0 9 * * 1-5", openMinutes: 120, tz: "Asia/Tokyo", openAfter: 0 });
    } finally {
      zone.mockRestore();
      clock.mockRestore();
    }
  });

  it("a threshold asks for how many notes, and sends no window", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ openCron: "0 9 * * *", openMinutes: 30, tz: "UTC" })] };
    await render();
    await openSettingsDialog();
    // The saved window is what the dialog opens on, with its zone kept.
    expect(radio(t("memo.q_when_window")).checked).toBe(true);
    expect(field("memo-q-tz").value).toBe("UTC");
    await click(radio(t("memo.q_when_threshold")));
    await setValue(field("memo-q-open-after"), "101");
    await save();
    expect(dialog().textContent).toContain(t("memo.q_err_open_after", { max: 100 }));
    await setValue(field("memo-q-open-after"), "5");
    await save();
    expect(bodySent()).toMatchObject({ openCron: "", openMinutes: 0, tz: "", openAfter: 5 });
  });

  it("checks the runs per day and the overdue hours, and sends every setting but nothing read-only", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true, pausedReason: "x", noReportAt: "t", maxTaskHours: 6 })] };
    await render();
    await openSettingsDialog();
    await setValue(field("memo-q-fires"), "201");
    await setValue(field("memo-q-task-hours"), "0");
    await save();
    expect(dialog().textContent).toContain(t("memo.q_err_fires", { max: 200 }));
    expect(dialog().textContent).toContain(t("memo.q_err_task_hours", { max: 72 }));
    await setValue(field("memo-q-fires"), "50");
    await setValue(field("memo-q-task-hours"), "24");
    await save();
    const sent = bodySent();
    expect(sent).toMatchObject({ route: "r-keep", maxFiresPerDay: 50, maxTaskHours: 24 });
    for (const readOnly of ["paused", "pausedAt", "pausedReason", "draining", "noReportAt", "generation", "categoryId"]) {
      expect(sent, readOnly).not.toHaveProperty(readOnly);
    }
  });

  it("says the switch means it runs by itself, and that Run now works either way", async () => {
    await render();
    await openSettingsDialog();
    expect(dialog().textContent).toContain(t("memo.q_enabled"));
    expect(dialog().textContent).toContain(t("memo.q_enabled_hint"));
  });

  it("shows the catalogue line alone when the save is refused, never the server's English", async () => {
    memoQueuePut.mockResolvedValue({ error: { code: "memo_queue_bad_settings", message: "openCron: bad cron field" } });
    await render();
    await openSettingsDialog();
    await save();
    expect(document.body.textContent).toContain(t("err.memo_queue_bad_settings"));
    expect(document.body.textContent).not.toContain("bad cron field");
  });
});

describe("a paused category", () => {
  it("is marked on the category with why, and Resume lifts the pause", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true, pausedAt: "2026-11-15T01:00:00Z", pausedReason: "the first turns of the last two runs ended badly" })] };
    await render();
    expect(document.querySelector(".memo-q-badge.paused")?.textContent).toBe(t("memo.autorun_badge_paused"));
    const notice = must(document.querySelector<HTMLElement>(".memo-q-paused"), "notice");
    expect(notice.textContent).toContain(t("memo.paused_notice"));
    expect(document.body.textContent).not.toContain("ended badly"); // the server's sentence is English
    await click(must(notice.querySelector("button"), "resume"));
    expect(memoQueueResume).toHaveBeenCalledWith("c1");
    expect(document.body.textContent).toContain(t("memo.paused_resume_done"));
  });

  it("is also said in the run history, with the same button", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await click(byLabel(t("memo.autorun_deliveries")));
    expect(dialog().querySelector(".memo-q-paused")?.textContent).toContain(t("memo.paused_notice"));
    expect([...dialog().querySelectorAll<HTMLButtonElement>("button")].some((b) => b.textContent?.includes(t("memo.d_fire")))).toBe(false);
    await click(must(dialog().querySelector(".memo-q-paused button"), "resume"));
    expect(memoQueueResume).toHaveBeenCalledWith("c1");
  });

  it("shows the catalogue line when resuming is refused", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true })] };
    memoQueueResume.mockResolvedValue({ error: { code: "not_found", message: "queue not found" } });
    await render();
    await click(must(document.querySelector(".memo-q-paused button"), "resume"));
    expect(document.body.textContent).not.toContain("queue not found");
  });

  it("says nothing for a category that is not paused", async () => {
    await render();
    expect(document.querySelector(".memo-q-badge.paused")).toBeNull();
    expect(document.querySelector(".memo-q-paused")).toBeNull();
  });
});

describe("a workspace that cannot run by itself yet", () => {
  it("says it has to be restarted, on the category and in the history, while Run now stays", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ noReportAt: "2026-11-15T01:00:00Z" })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    expect(document.querySelector(".memo-q-noreport")?.textContent).toBe(t("memo.no_report_notice"));
    await click(byLabel(t("memo.autorun_deliveries")));
    expect(dialog().querySelector(".memo-q-noreport")?.textContent).toBe(t("memo.no_report_notice"));
    expect(headMenuItem({ cls: "memo-cat-run" }).getAttribute("aria-disabled")).toBeNull();
  });

  it("stays quiet while auto-run is off, since nothing waits to run by itself", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ noReportAt: "t", enabled: false })] };
    await render();
    expect(document.querySelector(".memo-q-noreport")).toBeNull();
  });
});

describe("why nothing starts", () => {
  const until = "2026-11-16T09:30:00Z";
  const cases: [string, Record<string, unknown>, string, Record<string, string | number>][] = [
    ["tenant_off", {}, "memo.waiting_tenant_off", {}],
    ["disabled", {}, "memo.waiting_disabled", {}],
    ["window_closed", { until }, "memo.waiting_window_closed_at", { at: "" }],
    ["window_closed", {}, "memo.waiting_window_closed", {}],
    ["below_threshold", { detail: { needed: 5, waiting: 2 } }, "memo.waiting_below_threshold", { needed: 5, count: 2 }],
    ["quiet_minute", { until }, "memo.waiting_quiet_at", { at: "" }],
    ["daily_cap", { until }, "memo.waiting_cap_at", { at: "" }],
    ["daily_cap", {}, "memo.waiting_cap", {}],
    ["backoff", { until }, "memo.waiting_backoff_at", { at: "" }],
    ["usage_limit", { detail: { agentKind: "claude" } }, "memo.waiting_usage_limit", { agent: "Claude" }],
    ["no_place", { detail: { by: "category" } }, "memo.waiting_no_place", {}],
    ["no_place", { detail: { by: "member" } }, "memo.waiting_no_place_member", {}],
  ];
  const line = async (waiting: unknown) => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ waiting })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
  };
  const expected = (key: string, params: Record<string, string | number>) =>
    t(key as never, "at" in params ? { at: fmtDateTime(until) } : params);

  it.each(cases)("says %s in one line on the category and at the top of the run history", async (reason, extra, key, params) => {
    await line({ reason, ...extra });
    const want = expected(key, params);
    const onRow = [...document.querySelectorAll(".memo-q-why")];
    expect(onRow).toHaveLength(1);
    expect(onRow[0].textContent).toBe(want);
    await click(byLabel(t("memo.autorun_deliveries")));
    expect(dialog().querySelector(".memo-q-why")?.textContent).toBe(want);
  });

  it.each(["daily_cap", "usage_limit", "backoff", "no_place", "tenant_off"])("draws %s as a warning", async (reason) => {
    await line({ reason, detail: { agentKind: "claude" } });
    const el = document.querySelector(".memo-q-why");
    expect(el?.classList.contains("memo-q-why-warn")).toBe(true);
    expect(el?.querySelector(".codicon-warning")).not.toBeNull();
  });

  it.each(["window_closed", "below_threshold", "quiet_minute", "prerequisites", "deferred", "tidying", "disabled"])(
    "keeps %s muted",
    async (reason) => {
      await line({ reason });
      const el = document.querySelector(".memo-q-why");
      expect(el).not.toBeNull();
      expect(el?.classList.contains("memo-q-why-warn")).toBe(false);
      expect(el?.querySelector(".codicon-warning")).toBeNull();
    },
  );

  it("hints at the way out of the daily cap", async () => {
    await line({ reason: "daily_cap" });
    expect(document.querySelector(".memo-q-why-hint")?.textContent).toBe(t("memo.waiting_cap_hint"));
  });

  it("gives no cap hint for another warning reason", async () => {
    await line({ reason: "backoff" });
    expect(document.querySelector(".memo-q-why-hint")).toBeNull();
  });

  it("writes the time in the member's local form, not the raw stamp", async () => {
    await line({ reason: "daily_cap", until });
    expect(document.querySelector(".memo-q-why")?.textContent).not.toContain("2026-11-16T");
  });

  it("says nothing without a waiting field, as an older Control Plane answers", async () => {
    await line(undefined);
    expect(document.querySelector(".memo-q-why")).toBeNull();
    await click(byLabel(t("memo.autorun_deliveries")));
    expect(dialog().querySelector(".memo-q-why")).toBeNull();
  });

  it("drops a reason it does not know instead of showing an identifier", async () => {
    await line({ reason: "a_reason_from_the_future" });
    expect(document.querySelector(".memo-q-why")).toBeNull();
  });

  it("leaves a pause and a workspace to restart to their own notices and does not say them twice", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true, waiting: { reason: "paused" } })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    expect(document.querySelectorAll(".memo-q-paused")).toHaveLength(1);
    expect(document.querySelector(".memo-q-why")).toBeNull();
    servedQueues = { tenantEnabled: true, queues: [aQueue({ noReportAt: "t", waiting: { reason: "no_report" } })] };
    await bump();
    expect(document.querySelectorAll(".memo-q-noreport")).toHaveLength(1);
    expect(document.querySelector(".memo-q-why")).toBeNull();
  });

  it("reads the same lines in Japanese, with no stray key left", async () => {
    const { setLocale } = await import("../../lib/i18n/index.ts");
    setLocale("ja");
    try {
      for (const [reason, extra, key, params] of cases) {
        await line({ reason, ...extra });
        const text = document.querySelector(".memo-q-why")?.textContent ?? "";
        expect(text, reason).toBe(expected(key, params));
        expect(text, reason).not.toMatch(/memo\.waiting|[{}]/);
        expect(text, reason).toMatch(/[ぁ-ん]/);
        await act(async () => root!.render(<div />));
      }
    } finally {
      setLocale("en");
    }
  });
});

describe("the run history of a run that is still open", () => {
  const open = (over: Record<string, unknown>) => ({
    id: "d1", state: "open", outcome: "", session: "run-1", messageIds: ["a"], createdAt: "2026-11-15T01:49:03.708526Z",
    sentAt: "", closedAt: "", heldBy: "", heldSession: "", heldSeenAt: "", heldWakers: [], overdue: false, overdueAt: "", auto: false, retire: "",
    firstTurn: null, ...over,
  });
  const rowText = async (d: Record<string, unknown>) => {
    servedDeliveries = [open(d)];
    await render();
    await click(byLabel(t("memo.autorun_deliveries")));
    return must(dialog().querySelector<HTMLElement>(".memo-d-row"), "row");
  };

  it.each([
    ["machine", "memo.d_held_machine"],
    ["person", "memo.d_held_person"],
    ["limit", "memo.d_held_limit"],
    ["wakers", "memo.d_held_wakers"],
    ["unknown", "memo.d_held_unknown"],
    ["a-reason-from-a-newer-cp", "memo.d_held_unknown"],
  ] as const)("says why in words for %s", async (heldBy, key) => {
    const row = await rowText({ heldBy });
    expect(row.querySelector(".memo-d-held")?.textContent).toBe(t("memo.d_held", { why: t(key) }));
    expect(row.textContent).not.toContain(heldBy === "a-reason-from-a-newer-cp" ? heldBy : "\u0000");
  });

  it("names the watch that holds a run", async () => {
    const named = await rowText({ heldBy: "wakers", heldWakers: [{ id: "w_42", source: "github" }, { id: "t_1", source: "timer", timer: true }] });
    expect(named.querySelector(".memo-d-held")?.textContent).toBe(t("memo.d_held", { why: t("memo.d_held_wakers_named", { names: "w_42 (github), t_1 (timer)" }) }));
  });

  it.each([[undefined], [null], ["x"], [[{ nope: 1 }]], [[]]])("reads a missing or malformed detail (%j) as no names", async (heldWakers) => {
    const row = await rowText({ heldBy: "wakers", heldWakers });
    expect(row.querySelector(".memo-d-held")?.textContent).toBe(t("memo.d_held", { why: t("memo.d_held_wakers") }));
  });

  it("says nothing of a reason when the Agent has not reported", async () => {
    const row = await rowText({ heldBy: "" });
    expect(row.querySelector(".memo-d-held")).toBeNull();
  });

  it("names the session that holds it when it is not the run's own, and opens it", async () => {
    const row = await rowText({ heldBy: "person", heldSession: "child-7" });
    await click(must(row.querySelector(".memo-d-held-session"), "held session"));
    expect(openSessionChat).toHaveBeenCalledWith("child-7");
  });

  it("marks an overdue run, whether it started by itself, its first turn and a session taken over", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ maxTaskHours: 6 })] };
    const row = await rowText({ overdue: true, overdueAt: "2026-11-15T09:00:00Z", auto: true, retire: "released", firstTurn: { outcome: "turn-failed", ms: 900, endedAt: "x" } });
    const tag = must(row.querySelector<HTMLElement>(".memo-d-tag.overdue"), "overdue");
    expect(tag.textContent).toBe(t("memo.d_overdue"));
    expect(tag.title).toContain(t("memo.d_overdue_title", { hours: 6 }));
    expect(row.textContent).toContain(t("memo.d_auto"));
    expect(row.textContent).toContain(t("memo.d_turn_failed"));
    expect(row.textContent).toContain(t("memo.d_taken_over"));
  });

  it("says a run started with Run now, and how each first turn ended", async () => {
    const row = await rowText({ firstTurn: { outcome: "clean", ms: 1, endedAt: "x" } });
    expect(row.textContent).toContain(t("memo.d_manual"));
    expect(row.textContent).toContain(t("memo.d_turn_clean"));
    expect(row.textContent).not.toContain(t("memo.d_overdue"));
  });

  it.each([
    ["done", "memo.d_answer_done", false],
    ["partial", "memo.d_answer_partial", true],
    ["other", "memo.d_answer_other", true],
    ["unknown", "memo.d_answer_unknown", true],
    ["a-class-from-a-newer-cp", "memo.d_answer_plain", true],
  ] as const)("shows the run's last answer head with its class %s (#4876)", async (answerClass, key, open) => {
    const head = answerClass === "unknown" ? "" : "Partial: #4876 part 2, next part queued";
    const row = await rowText({ state: "closed", outcome: "clean", answer: head, answerClass });
    const el = must(row.querySelector<HTMLElement>(".memo-d-answer"), "answer");
    expect(el.textContent).toBe(t(key) + head);
    expect(el.querySelector(".memo-d-answer-head")?.textContent ?? "").toBe(head);
    expect(el.classList.contains("memo-d-answer--open")).toBe(open);
    expect(el.title).toContain(t("memo.d_answer_title"));
  });

  it("says nothing of a last answer before it was read", async () => {
    const row = await rowText({ answer: "", answerClass: "" });
    expect(row.querySelector(".memo-d-answer")).toBeNull();
    const old = await rowText({});
    expect(old.querySelector(".memo-d-answer")).toBeNull();
  });

  it("shows the start in the Console's local form, never the raw timestamp (#4208)", async () => {
    const row = await rowText({});
    const at = row.querySelector(".memo-d-at")!.textContent!;
    const { fmtDateTime } = await import("../../lib/intl.ts");
    expect(at).toBe(fmtDateTime("2026-11-15T01:49:03.708526Z"));
    expect(at).not.toContain("T01:49:03");
    expect(at).not.toContain("Z");
  });
});

describe("a refused Run now shows the catalogue line alone (#4208)", () => {
  it("drops the server's English sentence", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    memoQueueFire.mockResolvedValue({ error: { code: "memo_queue_no_slot", message: "every slot of this queue is taken (1 of 1)" } });
    await render();
    await click(headMenuItem({ cls: "memo-cat-run" }));
    expect(document.body.textContent).toContain(t("err.memo_queue_no_slot"));
    expect(document.body.textContent).not.toContain("every slot of this queue");
  });

  it("does the same for a refused approval", async () => {
    servedMemos = [held("m1", "h1")];
    memoRelease.mockResolvedValue({ error: { code: "memo_queues_disabled", message: "queues are off for this tenant" } });
    await render();
    await click(must(document.querySelector(".memo-release button"), "release"));
    expect(document.body.textContent).toContain(t("err.memo_queues_disabled"));
    expect(document.body.textContent).not.toContain("queues are off");
  });
});

describe("a note that changed while its card was on screen (#4209)", () => {
  const card = () => must(document.querySelector<HTMLElement>(".memo-release"), "card");
  const press = async () => click(must(card().querySelector(".memo-release-foot button"), "button"));

  it("approves a card that never changed in one press", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    await bump(); // a poll that brings the same hash
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(0);
    await press();
    expect(memoRelease).toHaveBeenCalledTimes(1);
  });

  it("marks the note, holds the approval until the change is confirmed, then approves what is shown", async () => {
    servedMemos = [held("m1", "h1"), held("m2", "h2")];
    await render();
    servedMemos = [held("m1", "h1"), held("m2", "h2-new", { body: "rewritten" })];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    expect(card().textContent).toContain(t("memo.release_edited"));
    expect(card().textContent).toContain("rewritten");

    await press(); // confirms, does not approve
    expect(memoRelease).not.toHaveBeenCalled();
    expect(card().textContent).toContain(t("memo.release_confirmed"));
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1); // still marked until approved

    await press();
    expect(memoRelease).toHaveBeenCalledWith([
      { id: "m1", contentHash: "h1" },
      { id: "m2", contentHash: "h2-new" },
    ]);
  });

  it("asks again when it changes once more after it was confirmed", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    await press();
    servedMemos = [held("m1", "h3")];
    await bump();
    await press();
    expect(memoRelease).not.toHaveBeenCalled();
    await press();
    expect(memoRelease).toHaveBeenCalledWith([{ id: "m1", contentHash: "h3" }]);
  });

  it("keeps the mark when the hash goes back to the first one (h1 -> h2 -> h1)", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    servedMemos = [held("m1", "h1")];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    await press(); // confirms the text shown now
    expect(memoRelease).not.toHaveBeenCalled();
    await press();
    expect(memoRelease).toHaveBeenCalledWith([{ id: "m1", contentHash: "h1" }]);
  });

  it("does not reuse a confirmation after the text moved on (h1 -> h2 confirmed -> h3 -> h2)", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    await press();
    servedMemos = [held("m1", "h3")];
    await bump();
    servedMemos = [held("m1", "h2")];
    await bump();
    await press();
    expect(memoRelease).not.toHaveBeenCalled();
    await press();
    expect(memoRelease).toHaveBeenCalledTimes(1);
  });

  it("keeps the record when the section is drawn again", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    act(() => root?.unmount());
    root = createRoot(host);
    await render();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    await press();
    expect(memoRelease).not.toHaveBeenCalled();
  });

  it("keeps the record when the note leaves the card and comes back with another text", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [aMemo({ id: "m1", state: "pending", provenance: "member" })];
    await bump();
    expect(document.querySelector(".memo-release")).toBeNull();
    servedMemos = [held("m1", "h9")]; // a session edited it after it was approved
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    await press();
    expect(memoRelease).not.toHaveBeenCalled();
  });

  it("keeps a tenant's record across a switch to another tenant and back (A -> B -> A)", async () => {
    const { useTenantStore } = await import("../../core/store/tenant.ts");
    const switchTo = async (tenant: string) => {
      await act(async () => useTenantStore.setState({ tenant }));
      await settle();
    };
    await switchTo("a");
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    servedMemos = [held("x1", "b1")]; // tenant b's own notes
    await switchTo("b");
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(0);
    servedMemos = [held("m1", "h2")];
    await switchTo("a");
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    await press(); // confirms; does not approve
    expect(memoRelease).not.toHaveBeenCalled();
    await switchTo("");
  });

  it("keeps the mark through a failed read of the notes (an error body is not an empty list)", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    servedMemos = { error: { code: "http_502", message: "bad gateway" } } as unknown as Memo[];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1); // still on screen, still marked
    servedMemos = [held("m1", "h2")];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
    await press();
    expect(memoRelease).not.toHaveBeenCalled();
  });

  it("a failed first read does not count as a loaded list, so the first good read starts the records", async () => {
    servedMemos = { error: { code: "http_502" } } as unknown as Memo[];
    await render();
    servedMemos = [held("m1", "h1")];
    await bump();
    expect(document.querySelector(".memo-release")).not.toBeNull();
  });

  it("starts afresh after a page reload and once the note was approved", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    servedMemos = [held("m1", "h2")];
    await bump();
    act(() => root?.unmount());
    resetShownNotes(); // a reload: module memory is gone
    root = createRoot(host);
    await render();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(0);
    await press();
    expect(memoRelease).toHaveBeenCalledTimes(1);
    // Approved: the record is forgotten, so the same note held again with other text is a new card.
    servedMemos = [held("m1", "h5")];
    await bump();
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(0);
  });

  it("still marks the notes a refused stale release names", async () => {
    servedMemos = [held("m1", "h1")];
    await render();
    memoRelease.mockResolvedValueOnce({ error: { code: "memo_release_stale", stale: ["m1"] } });
    await press();
    expect(card().classList.contains("changed")).toBe(true);
    expect(card().querySelectorAll(".memo-release-changed")).toHaveLength(1);
  });
});

describe("a notification asks to show a category", () => {
  it("opens the category's run history, and a pause shows on the category without it", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true })] };
    localStorage.setItem("af-section-memos", "0"); // the section was folded
    await render();
    const { useMemoStore } = await import("./store.ts");
    await act(async () => useMemoStore.getState().revealCategory("c1", true));
    await settle();
    expect(memoQueueDeliveries).toHaveBeenCalledWith("c1");
    expect(dialog().textContent).toContain(t("memo.autorun_deliveries_title", { name: "inbox-q" }));
    localStorage.removeItem("af-section-memos");
  });
});

describe("full run history dialog", () => {
  const run = (id: string, over: Record<string, unknown> = {}) => ({
    id, state: "closed", outcome: "clean", session: "s-" + id, messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "", auto: true, ...over,
  });

  it("has no Show in Auto-run link; pressing the session opens it", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    const { useMemoStore } = await import("./store.ts");
    useSessionsStore.setState({ sessions: [{ name: "s-d1", kind: "claude", alive: false, origin: "queue" }] as never });
    servedQueues = { tenantEnabled: true, queues: [aQueue()] };
    servedDeliveries = [run("d1")];
    await render();
    await act(async () => useMemoStore.getState().revealCategory("c1", true));
    await settle();
    expect(dialog().querySelector(".memo-d-show-run")).toBeNull();
    expect(dialog().querySelector(".memo-d-session")).not.toBeNull();
    useSessionsStore.setState({ sessions: [] });
  });
});

describe("inline run list under a category", () => {
  const toggle = () => must([...document.querySelectorAll<HTMLElement>(".memo-runs-toggle")][0], "runs toggle");
  const run = (id: string, over: Record<string, unknown> = {}) => ({
    id, state: "closed", outcome: "clean", session: "s-" + id, messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "", auto: true, ...over,
  });

  it("reads nothing while collapsed, then lists the runs on expand and collapses again", async () => {
    servedDeliveries = [run("d1"), run("d2", { state: "open", outcome: "", heldBy: "wakers", session: "", heldWakers: [{ id: "w_9", source: "slack" }] })];
    await render();
    expect(memoQueueDeliveries).not.toHaveBeenCalled();
    expect(document.querySelector(".memo-runs")).toBeNull();
    await click(toggle());
    expect(memoQueueDeliveries).toHaveBeenCalledWith("c1");
    const rows = document.querySelectorAll(".memo-run");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain(t("memo.d_state_clean"));
    expect(rows[0].textContent).toContain(t("memo.d_auto_short"));
    expect(rows[1].textContent).toContain(t("memo.d_state_running"));
    expect(rows[1].textContent).toContain(t("memo.d_held_wakers_named", { names: "w_9 (slack)" }));
    expect(rows[1].querySelector<HTMLElement>(".memo-run-held")?.title).toBe(t("memo.d_held_wakers_named", { names: "w_9 (slack)" }));
    expect(document.querySelector(".ui-modal")).toBeNull();
    await click(toggle());
    expect(document.querySelector(".memo-runs")).toBeNull();
  });

  it("puts a run's last answer, class and head, in its tooltip, and nothing for a run without one (#4876)", async () => {
    servedDeliveries = [run("d1", { answer: "Held: #4876 part 2, PR #9 not merged", answerClass: "partial" }), run("d2")];
    await render();
    await click(toggle());
    const rows = document.querySelectorAll<HTMLElement>(".memo-run");
    expect(rows[0].title).toContain(t("memo.d_answer_partial") + ": Held: #4876 part 2, PR #9 not merged");
    for (const k of ["memo.d_answer_done", "memo.d_answer_partial", "memo.d_answer_other", "memo.d_answer_unknown", "memo.d_answer_plain"] as const) {
      expect(rows[1].title).not.toContain(t(k));
    }
  });

  it("has no Show in Auto-run link in a run row", async () => {
    const { useSessionsStore } = await import("../sessions/store.ts");
    useSessionsStore.setState({ sessions: [{ name: "s-d1", kind: "claude", alive: false, origin: "queue" }] as never });
    servedDeliveries = [run("d1")];
    await render();
    await click(toggle());
    expect(document.querySelectorAll(".memo-run")).toHaveLength(1);
    expect(document.querySelector(".memo-run-view")).toBeNull();
    useSessionsStore.setState({ sessions: [] });
  });

  it("opens a run's session from its row, by click and by Enter, and leaves a sessionless run inert", async () => {
    servedDeliveries = [run("d1"), run("d2", { session: "", outcome: "failed" })];
    await render();
    await click(toggle());
    const rows = document.querySelectorAll<HTMLElement>(".memo-run");
    await click(rows[0]);
    expect(openSessionChat).toHaveBeenCalledWith("s-d1");
    openSessionChat.mockClear();
    await act(async () => {
      rows[0].dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    });
    expect(openSessionChat).toHaveBeenCalledWith("s-d1");
    openSessionChat.mockClear();
    await act(async () => {
      rows[0].dispatchEvent(new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true }));
    });
    expect(openSessionChat).toHaveBeenCalledWith("s-d1");
    openSessionChat.mockClear();
    expect(rows[1].getAttribute("role")).toBeNull();
    await click(rows[1]);
    expect(openSessionChat).not.toHaveBeenCalled();
  });

  describe("ending an open run", () => {
    const openRun = () => run("d2", { state: "open", outcome: "", heldBy: "wakers", session: "s-d2" });
    const endBtn = () => document.querySelector<HTMLElement>(".memo-run-end");
    const confirmBtns = () => [...document.querySelectorAll<HTMLElement>(".ui-confirm-actions button")];

    it("offers it on an open run only, and asks first without opening the session", async () => {
      servedDeliveries = [run("d1"), openRun()];
      await render();
      await click(toggle());
      expect(document.querySelectorAll(".memo-run-end")).toHaveLength(1);
      await click(must(endBtn(), "end button"));
      expect(openSessionChat).not.toHaveBeenCalled();
      expect(memoQueueDeliveryEnd).not.toHaveBeenCalled();
      expect(document.querySelector(".ui-confirm")?.textContent).toContain(t("memo.d_end_body", { name: "s-d2" }));
      await click(confirmBtns()[0]); // cancel
      expect(memoQueueDeliveryEnd).not.toHaveBeenCalled();
    });

    it("ends the run on confirm, then re-reads the list", async () => {
      servedDeliveries = [openRun()];
      await render();
      await click(toggle());
      memoQueueDeliveries.mockClear();
      await click(must(endBtn(), "end button"));
      await click(must(confirmBtns().find((b) => b.textContent === t("memo.d_end_confirm")), "confirm"));
      expect(memoQueueDeliveryEnd).toHaveBeenCalledWith("c1", "d2");
      expect(memoQueueDeliveries).toHaveBeenCalledWith("c1");
    });

    it("says so when the Control Plane refuses", async () => {
      servedDeliveries = [openRun()];
      memoQueueDeliveryEnd.mockResolvedValueOnce({ error: { code: "delivery_not_open", message: "nope" } });
      await render();
      await click(toggle());
      await click(must(endBtn(), "end button"));
      await click(must(confirmBtns().find((b) => b.textContent === t("memo.d_end_confirm")), "confirm"));
      expect(memoQueueDeliveryEnd).toHaveBeenCalledTimes(1);
      expect(document.body.textContent).toContain("nope");
    });
  });

  it("shows the empty and failed states", async () => {
    servedDeliveries = [];
    await render();
    await click(toggle());
    expect(document.querySelector(".memo-runs")?.textContent).toContain(t("memo.d_empty"));
    await click(toggle());
    servedDeliveries = { error: { code: "x", message: "boom" } };
    await click(toggle());
    expect(document.querySelector(".memo-runs [role=alert]")?.textContent).toContain(t("memo.d_load_failed"));
  });

  it("keeps the last good rows when a later poll fails, and clears the alert when it recovers", async () => {
    servedDeliveries = [run("d1")];
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      await render();
      await click(toggle());
      expect(document.querySelectorAll(".memo-run")).toHaveLength(1);
      servedDeliveries = { error: { code: "x", message: "boom" } };
      await act(async () => void vi.advanceTimersByTime(30000));
      await settle();
      expect(document.querySelectorAll(".memo-run")).toHaveLength(1);
      expect(document.querySelector(".memo-runs [role=alert]")).not.toBeNull();
      servedDeliveries = [run("d1")];
      await act(async () => void vi.advanceTimersByTime(30000));
      await settle();
      expect(document.querySelector(".memo-runs [role=alert]")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("caps the list at five and sends \"show all\" to the full dialog", async () => {
    servedDeliveries = ["1", "2", "3", "4", "5", "6", "7"].map((n) => run("d" + n));
    await render();
    await click(toggle());
    expect(document.querySelectorAll(".memo-run")).toHaveLength(5);
    await click(must(document.querySelector(".memo-runs-all"), "show all"));
    expect(dialog().querySelectorAll(".memo-d-row")).toHaveLength(7);
  });
});

describe("the category head", () => {
  const head = () => must(document.querySelector<HTMLElement>(".memo-cat-head"), "head");

  it("keeps the caret, checkbox, name, count, one badge, + and ⋯; the actions are in the menu", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    expect(head().querySelectorAll(".memo-cat-act, .memo-cat-run, .memo-cat-send, .memo-cat-del, .memo-cat-grip")).toHaveLength(0);
    expect(head().querySelectorAll(".memo-q-badge")).toHaveLength(1);
    expect(head().querySelector(".memo-q-badge")?.getAttribute("aria-label")).toBe(t("memo.autorun_badge_paused"));
    expect(document.querySelector(".memo-cat-menu")).toBeNull();
    await click(must(head().querySelector(".memo-cat-more"), "more"));
    const labels = [...document.querySelectorAll(".memo-cat-menu .ui-menu-item")].map((e) => e.textContent ?? "");
    expect(labels).toHaveLength(7);
    expect(labels[0]).toContain(t("common.send"));
    expect(labels[1]).toContain(t("memo.cat_menu_run"));
    expect(labels[4]).toContain(t("memo.rename_category_item"));
    expect(labels[5]).toContain(t("memo.clear_finished"));
    expect(labels[6]).toContain(t("memo.delete_category"));
    // The rename input's class styles a bordered text field: the menu item must not carry it.
    expect(document.querySelector(".memo-cat-menu .memo-cat-rename")).toBeNull();
  });

  it("opens the same menu on right-click and on Shift+F10, and does not fold the category", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    const expanded = () => head().querySelector("[aria-expanded]")?.getAttribute("aria-expanded");
    const before = expanded();
    await act(async () => {
      head().dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    expect(document.querySelector(".memo-cat-menu")).not.toBeNull();
    expect(expanded()).toBe(before);
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector(".memo-cat-menu")).toBeNull();
    await act(async () => {
      head().dispatchEvent(new KeyboardEvent("keydown", { key: "F10", shiftKey: true, bubbles: true, cancelable: true }));
    });
    expect(document.querySelector(".memo-cat-menu")).not.toBeNull();
  });

  it("counts only the waiting memos of an auto-run category, and every memo of a plain one", async () => {
    servedCats = [aCat({ queue: true }), aCat({ id: "c2", name: "plain", position: 1 })];
    servedMemos = [
      aMemo({ id: "w", state: "pending", provenance: "member" }),
      aMemo({ id: "d", state: "delivered" }),
      aMemo({ id: "s", state: "superseded" }),
      aMemo({ id: "p1", category: "plain" }),
      aMemo({ id: "p2", category: "plain" }),
    ];
    await render();
    const n = (name: string) =>
      [...document.querySelectorAll<HTMLElement>(".memo-cat")].find((g) => g.querySelector(".memo-cat-name")?.textContent === name)?.querySelector(".memo-cat-n")?.textContent;
    expect(n("inbox-q")).toBe("1");
    expect(n("plain")).toBe("2");
  });

  it("offers Clear finished memos in an auto-run category only", async () => {
    servedCats = [aCat({ queue: true }), aCat({ id: "c2", name: "plain", position: 1 })];
    servedMemos = [aMemo({ id: "d", state: "delivered" }), aMemo({ id: "p1", category: "plain" })];
    await render();
    const menuOf = async (name: string) => {
      const g = [...document.querySelectorAll<HTMLElement>(".memo-cat")].find((x) => x.querySelector(".memo-cat-name")?.textContent === name);
      await click(must(g?.querySelector(".memo-cat-more"), "more"));
      const has = document.querySelector(".memo-cat-menu .memo-cat-menu-clear") !== null;
      await act(async () => {
        document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
      return has;
    };
    expect(await menuOf("inbox-q")).toBe(true);
    expect(await menuOf("plain")).toBe(false);
  });

  it("is the drag handle of a named category, folds on click, and the uncategorised bucket is not draggable", async () => {
    servedCats = [aCat({})];
    servedMemos = [aMemo({ id: "a" }), aMemo({ id: "u", category: "" })];
    await render();
    const heads = [...document.querySelectorAll<HTMLElement>(".memo-cat-head")];
    expect(heads).toHaveLength(2);
    const named = heads.find((h) => h.querySelector(".memo-cat-name")?.textContent === "inbox-q")!;
    const loose = heads.find((h) => h !== named)!;
    expect(named.draggable).toBe(true);
    expect(loose.draggable).toBe(false);
    const expanded = () => named.querySelector("[aria-expanded]")?.getAttribute("aria-expanded");
    const before = expanded();
    await click(named);
    expect(expanded()).not.toBe(before);
  });

  it("keeps the menu items free of the head's hover-only button classes (they are portalled out of the head)", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await click(must(head().querySelector(".memo-cat-more"), "more"));
    expect(document.querySelectorAll(".memo-cat-menu .memo-cat-send, .memo-cat-menu .memo-cat-del, .memo-cat-menu .memo-cat-act")).toHaveLength(0);
  });

  it("gives the uncategorised bucket its own Send in the head", async () => {
    servedCats = [];
    servedMemos = [aMemo({ id: "u", category: "" })];
    await render();
    expect(head().querySelector(".memo-cat-send")).not.toBeNull();
    expect(head().querySelector(".memo-cat-more")).toBeNull();
  });

  it("keeps Run now disabled while a press is in flight, also after the menu closed and reopened", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    let finish: (v: unknown) => void = () => {};
    memoQueueFire.mockReturnValue(new Promise((r) => (finish = r)));
    await render();
    await click(headMenuItem({ cls: "memo-cat-run" }));
    expect(document.querySelector(".memo-cat-menu")).toBeNull();
    expect(headMenuItem({ cls: "memo-cat-run" }).getAttribute("aria-disabled")).toBe("true");
    await click(headMenuItem({ cls: "memo-cat-run" }));
    expect(memoQueueFire).toHaveBeenCalledTimes(1);
    expect(headMenuItem({ cls: "memo-cat-run" }).textContent).toContain(t("memo.cat_menu_run_busy"));
    expect(headMenuItem({ cls: "memo-cat-run" }).querySelector(".memo-cat-menu-why")).not.toBeNull();
    // The reopened menu is still showing when the request fails: it must re-enable by itself.
    await act(async () => finish({ error: { code: "memo_queue_failed", message: "x" } }));
    await settle();
    expect(document.querySelector(".memo-cat-menu")).not.toBeNull();
    expect(headMenuItem({ cls: "memo-cat-run" }).getAttribute("aria-disabled")).toBeNull();
  });

  it("sends the category's current memos, not the ones it had when the menu opened", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await click(must(head().querySelector(".memo-cat-more"), "more"));
    expect(document.querySelector(".memo-cat-menu .ui-menu-item")?.textContent).toContain("(1)");
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" }), aMemo({ id: "b", state: "pending", provenance: "member", position: 1 })];
    await bump();
    expect(document.querySelector(".memo-cat-menu .ui-menu-item")?.textContent).toContain("(2)");
  });

  it("returns focus to ⋯ when the menu is closed with Escape", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    const more = must(head().querySelector<HTMLElement>(".memo-cat-more"), "more");
    more.focus();
    await click(more);
    must(document.querySelector<HTMLElement>(".memo-cat-menu .ui-menu-item"), "item").focus();
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(document.querySelector(".memo-cat-menu")).toBeNull();
    expect(document.activeElement).toBe(more);
  });

  const sendText = () => must(document.querySelector<HTMLTextAreaElement>(".ui-modal textarea"), "send text").value;

  it("Send from an open menu takes the category's current memos", async () => {
    servedMemos = [aMemo({ id: "a", body: "first-body", state: "pending", provenance: "member" })];
    await render();
    await click(must(head().querySelector(".memo-cat-more"), "more"));
    servedMemos = [
      aMemo({ id: "a", body: "first-body", state: "pending", provenance: "member" }),
      aMemo({ id: "b", body: "second-body", state: "pending", provenance: "member", position: 1 }),
    ];
    await bump();
    await click(headMenuItem({ cls: "memo-cat-menu-send" }));
    expect(sendText()).toContain("first-body");
    expect(sendText()).toContain("second-body");
  });

  it("the uncategorised Send opens the send dialog with that bucket's memos", async () => {
    servedCats = [];
    servedMemos = [aMemo({ id: "u1", category: "", body: "loose-one" }), aMemo({ id: "u2", category: "", body: "loose-two", position: 1 })];
    await render();
    await click(must(head().querySelector(".memo-cat-send"), "send"));
    expect(sendText()).toContain("loose-one");
    expect(sendText()).toContain("loose-two");
  });

  it("dragging a head onto another category reorders them, and does not fold either", async () => {
    servedCats = [aCat({}), aCat({ id: "c2", name: "plain", position: 1 })];
    servedMemos = [aMemo({ id: "a" }), aMemo({ id: "p", category: "plain" })];
    await render();
    const heads = [...document.querySelectorAll<HTMLElement>(".memo-cat-head")];
    const [first, second] = heads;
    const target = must(first.closest<HTMLElement>(".memo-cat"), "target group");
    const fold = () => heads.map((h) => h.querySelector("[aria-expanded]")?.getAttribute("aria-expanded"));
    const before = fold();
    const ev = (type: string) => {
      const e = new Event(type, { bubbles: true, cancelable: true });
      Object.assign(e, { dataTransfer: { setData() {}, getData: () => "", effectAllowed: "", dropEffect: "" } });
      return e;
    };
    await act(async () => {
      second.dispatchEvent(ev("dragstart"));
      target.dispatchEvent(ev("dragover"));
      target.dispatchEvent(ev("drop"));
      second.dispatchEvent(ev("dragend"));
    });
    await settle();
    expect(memoCategoryUpdateMock).toHaveBeenCalledWith("c2", { position: 0 });
    expect(memoCategoryUpdateMock).toHaveBeenCalledWith("c1", { position: 1 });
    expect(fold()).toEqual(before);
  });
});

describe("Return this run to the queue in the memo menu (#4877)", () => {
  const rowMenuBtn = () => must(document.querySelector<HTMLElement>(".memo-row .memo-menu-btn"), "row menu button");
  const retItem = () => document.querySelector<HTMLButtonElement>(".ui-menu .memo-menu-returnrun");
  const delivered = () => aMemo({ id: "a", state: "delivered", deliveryId: "d1" });
  const run = (over: Record<string, unknown> = {}) => ({
    id: "d1", state: "closed", outcome: "clean", session: "run-1", messageIds: ["a"], createdAt: "t1", sentAt: "", closedAt: "t2", ...over,
  });
  const openMenu = async () => {
    await render();
    await click(must(document.querySelector(".memo-sent-fold"), "history fold"));
    await click(rowMenuBtn());
  };

  it("returns the run of a delivered memo after confirming", async () => {
    servedMemos = [delivered()];
    servedDeliveries = [run()];
    await openMenu();
    const item = must(retItem(), "return item");
    expect(item.disabled).toBe(false);
    await click(item);
    await click(must([...document.querySelectorAll<HTMLElement>(".ui-confirm-actions button")].find((b) => b.textContent === t("memo.d_return_confirm")), "confirm"));
    expect(memoQueueDeliveryReturn).toHaveBeenCalledWith("c1", "d1");
  });
  it("is disabled with the reason when the run cannot return", async () => {
    servedMemos = [delivered()];
    servedDeliveries = [run({ returnedAt: "2026-11-20T10:00:00Z" })];
    await openMenu();
    const item = must(retItem(), "return item");
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain(t("memo.menu_return_run_no"));
    await click(item);
    expect(memoQueueDeliveryReturn).not.toHaveBeenCalled();
  });
  it("is absent on a memo that no run delivered", async () => {
    servedMemos = [aMemo({ id: "p", state: "pending", provenance: "member" })];
    await render();
    await click(rowMenuBtn());
    expect(retItem()).toBeNull();
  });
  it("asks for open runs and the runs closed since the memo was sent, not the newest fifty alone", async () => {
    servedMemos = [aMemo({ id: "a", state: "delivered", deliveryId: "d1", sentAt: "2026-11-01T00:00:00Z" })];
    servedDeliveries = [run()];
    await openMenu();
    expect(memoQueueDeliveries).toHaveBeenCalledWith("c1", true, "2026-11-01T00:00:00Z");
  });
  it("is disabled and says where to look when the run is not in the answer", async () => {
    servedMemos = [delivered()];
    servedDeliveries = [run({ id: "other" })];
    await openMenu();
    const item = must(retItem(), "return item");
    expect(item.disabled).toBe(true);
    expect(item.textContent).toContain(t("memo.menu_return_run_missing"));
  });
  it("shows no item when the read fails", async () => {
    servedMemos = [delivered()];
    memoQueueDeliveries.mockRejectedValueOnce(new Error("boom"));
    await openMenu();
    expect(retItem()).toBeNull();
  });
  it("shows the item with the menuitem role once the read arrives", async () => {
    servedMemos = [delivered()];
    let release: (v: unknown) => void = () => {};
    memoQueueDeliveries.mockImplementationOnce(() => new Promise<never>((r) => (release = r as (v: unknown) => void)));
    await openMenu();
    const waiting = must(retItem(), "return item while loading");
    expect(waiting.disabled).toBe(true);
    expect(waiting.textContent).toContain(t("memo.menu_return_run_checking"));
    await act(async () => release([run()]));
    const ready = must(retItem(), "return item");
    expect(ready.getAttribute("role")).toBe("menuitem");
    expect(ready.disabled).toBe(false);
  });
});

describe("Stop chain in the memo menu", () => {
  const rowMenuBtn = () => must(document.querySelector<HTMLElement>(".memo-row .memo-menu-btn"), "row menu button");
  const stopItem = () => document.querySelector<HTMLElement>(".ui-menu .memo-menu-stopchain");
  const chained = (over: Partial<Memo> = {}) =>
    aMemo({ id: "f1", state: "pending", releasedBy: "continuation", chainId: "ch1", chainSeq: 1, ...over });

  for (const lang of ["en", "ja"] as const) {
    describe(lang, () => {
      beforeEach(() => setLocale(lang));
      afterEach(() => setLocale("en"));

      it("is a menu item, not a row button, and calls memoStopChain with the memo id", async () => {
        servedQueues = { tenantEnabled: true, queues: [aQueue({ continuationMax: 4 })] };
        servedMemos = [chained()];
        await render();
        expect(document.querySelector(".memo-row")?.textContent).not.toContain(t("memo.continuation_stop"));
        // The continuation badge stays in the row.
        expect(document.querySelector(".memo-row")?.textContent).toContain(t("memo.continuation_badge_of", { seq: 1, max: 4 }));
        await click(rowMenuBtn());
        const item = must(stopItem(), "stop chain item");
        expect(item.textContent).toContain(t("memo.continuation_stop"));
        expect(item.querySelector(".memo-cat-menu-why")?.textContent).toBe(t("memo.continuation_stop_title"));
        await click(item);
        expect(memoStopChain).toHaveBeenCalledWith("f1");
        expect(document.querySelector(".ui-menu")).toBeNull();
      });
    });
  }

  it("is also in the right-click menu", async () => {
    servedMemos = [chained()];
    await render();
    await act(async () => {
      must(document.querySelector(".memo-row"), "row").dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
    });
    expect(stopItem()).not.toBeNull();
  });

  it("is offered on a delivered memo too", async () => {
    servedMemos = [chained({ state: "delivered" })];
    await render();
    // Delivered notes are folded into one line until it is opened.
    await click(must(document.querySelector(".memo-sent-fold"), "history fold"));
    await click(rowMenuBtn());
    await click(must(stopItem(), "stop chain item"));
    expect(memoStopChain).toHaveBeenCalledWith("f1");
  });

  it("is offered on a root that has no chain id yet, while the category allows follow-ups", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ continuationMax: 4 })] };
    servedMemos = [aMemo({ id: "root", state: "pending", reference: { provider: "github", key: "o/r#1", url: "" } })];
    await render();
    await click(rowMenuBtn());
    await click(must(stopItem(), "stop chain item"));
    expect(memoStopChain).toHaveBeenCalledWith("root");
  });

  it("is absent on a root when the category allows no follow-ups, or the note has no work item", async () => {
    const ref = { provider: "github", key: "o/r#1", url: "" };
    servedQueues = { tenantEnabled: true, queues: [aQueue({ continuationMax: 0 })] };
    servedMemos = [aMemo({ id: "root", state: "pending", reference: ref })];
    await render();
    await click(rowMenuBtn());
    expect(stopItem()).toBeNull();
    servedQueues = { tenantEnabled: true, queues: [aQueue({ continuationMax: 4 })] };
    servedMemos = [aMemo({ id: "root", state: "pending" })];
    await bump();
    expect(stopItem()).toBeNull();
  });

  it("shows the item with the menuitem role when a refresh brings an open memo into reach", async () => {
    servedMemos = [chained({ state: "unconfirmed" })];
    await render();
    await click(rowMenuBtn());
    expect(stopItem()).toBeNull();
    servedMemos = [chained()];
    await bump();
    expect(must(stopItem(), "stop chain item").getAttribute("role")).toBe("menuitem");
  });

  it("drops the item when a refresh moves the open memo out of reach", async () => {
    servedMemos = [chained()];
    await render();
    await click(rowMenuBtn());
    expect(stopItem()).not.toBeNull();
    servedMemos = [chained({ state: "unconfirmed" })];
    await bump();
    expect(document.querySelector(".ui-menu")).not.toBeNull();
    expect(stopItem()).toBeNull();
  });

  it("is absent on a memo that is in no chain, and on one in neither state", async () => {
    servedMemos = [aMemo({ id: "plain", state: "pending" }), chained({ id: "done", state: "unconfirmed" })];
    await render();
    for (const btn of document.querySelectorAll<HTMLElement>(".memo-row .memo-menu-btn")) {
      await click(btn);
      expect(stopItem()).toBeNull();
      await click(btn);
    }
    expect(memoStopChain).not.toHaveBeenCalled();
  });
});

describe("Run next in the memo menu (#5021)", () => {
  const rowMenuBtn = () => must(document.querySelector<HTMLElement>(".memo-row .memo-menu-btn"), "row menu button");
  const nextItem = () => document.querySelector<HTMLElement>(".ui-menu .memo-menu-runnext");
  const badge = () => document.querySelector<HTMLElement>(".memo-row .memo-qrunnext");
  const waiting = (over: Partial<Memo> = {}) => aMemo({ id: "a", state: "pending", provenance: "member", ...over });

  for (const lang of ["en", "ja"] as const) {
    describe(lang, () => {
      beforeEach(() => setLocale(lang));
      afterEach(() => setLocale("en"));

      it("marks an unmarked waiting memo, with no badge before", async () => {
        servedMemos = [waiting()];
        await render();
        expect(badge()).toBeNull();
        await click(rowMenuBtn());
        const item = must(nextItem(), "run next item");
        expect(item.getAttribute("role")).toBe("menuitem");
        expect(item.textContent).toContain(t("memo.run_next"));
        expect(item.querySelector(".memo-cat-menu-why")?.textContent).toBe(t("memo.run_next_hint"));
        await click(item);
        expect(memoSetRunNext).toHaveBeenCalledWith("a", true);
        expect(document.body.textContent).toContain(t("memo.run_next_done"));
        expect(document.querySelector(".ui-menu")).toBeNull();
      });

      it("shows the badge on a marked memo and offers to clear it", async () => {
        servedMemos = [waiting({ runNext: true })];
        await render();
        const b = must(badge(), "run next badge");
        expect(b.textContent).toContain(t("memo.run_next_badge"));
        expect(b.getAttribute("title")).toBe(t("memo.run_next_badge_title"));
        await click(rowMenuBtn());
        const item = must(nextItem(), "clear run next item");
        expect(item.textContent).toContain(t("memo.run_next_clear"));
        await click(item);
        expect(memoSetRunNext).toHaveBeenCalledWith("a", false);
        expect(document.body.textContent).toContain(t("memo.run_next_cleared"));
      });
    });
  }

  it("is offered on a held memo too", async () => {
    servedMemos = [waiting({ state: "held", provenance: "workspace" })];
    await render();
    await click(rowMenuBtn());
    expect(nextItem()).not.toBeNull();
  });

  it("keeps arrow navigation working when a refresh removes the focused Run next item", async () => {
    // jsdom has no layout, so expose the rendered menu items to the visibility filter.
    const layout = vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
      return this.parentElement;
    });
    try {
      servedMemos = [waiting()];
      await render();
      await click(rowMenuBtn());
      const item = must(nextItem(), "run next item");
      const menu = must(item.closest(".ui-menu"), "row menu");
      item.focus();
      expect(document.activeElement).toBe(item);

      servedMemos = [waiting({ deliveryId: "d1" })];
      await bump();
      expect(nextItem()).toBeNull();
      expect(menu.contains(document.activeElement)).toBe(true);

      const focused = document.activeElement!;
      focused.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true, cancelable: true }));
      expect(menu.contains(document.activeElement)).toBe(true);
      expect(document.activeElement).not.toBe(focused);
    } finally {
      layout.mockRestore();
    }
  });

  for (const code of ["memo_locked", "memo_in_queue"]) {
    it(`says the memo is no longer waiting on ${code}, and reads the list again`, async () => {
      memoSetRunNext.mockResolvedValueOnce({ error: { code, message: "refused" } });
      servedMemos = [waiting()];
      await render();
      await click(rowMenuBtn());
      const before = memoListCalls;
      await click(must(nextItem(), "run next item"));
      expect(document.body.textContent).not.toContain(t("memo.run_next_done"));
      expect(document.querySelector(".ui-toast-msg")?.textContent).toContain(t("memo.run_next_not_waiting"));
      expect(memoListCalls).toBeGreaterThan(before);
    });
  }

  it("sends one write per memo until it answers, then offers the other way", async () => {
    servedMemos = [waiting()];
    await render();
    let answer: (v: unknown) => void = () => {};
    memoSetRunNext.mockImplementationOnce(() => new Promise((r) => (answer = r)));
    await click(rowMenuBtn());
    await click(must(nextItem(), "run next item"));
    await click(rowMenuBtn());
    const busy = must(nextItem(), "run next item while sending");
    expect(busy.getAttribute("aria-disabled")).toBe("true");
    await click(busy);
    expect(memoSetRunNext).toHaveBeenCalledTimes(1);
    servedMemos = [waiting({ runNext: true })];
    await act(async () => answer({ id: "a", runNext: true }));
    await click(rowMenuBtn());
    const clear = must(nextItem(), "clear run next item");
    expect(clear.getAttribute("aria-disabled")).toBeNull();
    expect(clear.textContent).toContain(t("memo.run_next_clear"));
    await click(clear);
    expect(memoSetRunNext).toHaveBeenCalledTimes(2);
    expect(memoSetRunNext).toHaveBeenLastCalledWith("a", false);
  });

  it("shows the answer at once when the re-read fails", async () => {
    servedMemos = [waiting()];
    await render();
    await click(rowMenuBtn());
    memoListRejects = true;
    try {
      await click(must(nextItem(), "run next item"));
      expect(memoSetRunNext).toHaveBeenLastCalledWith("a", true);
      expect(badge()).not.toBeNull();
      await click(rowMenuBtn());
      const clear = must(nextItem(), "clear run next item");
      expect(clear.textContent).toContain(t("memo.run_next_clear"));
      await click(clear);
      expect(memoSetRunNext).toHaveBeenLastCalledWith("a", false);
      expect(badge()).toBeNull();
    } finally {
      memoListRejects = false;
    }
  });

  it("is absent outside an auto-run category, and on a memo a run has taken or that already ran", async () => {
    servedCats = [aCat({})];
    servedQueues = { tenantEnabled: true, queues: [] };
    servedMemos = [waiting()];
    await render();
    await click(rowMenuBtn());
    expect(nextItem()).toBeNull();
    servedCats = [aCat({ queue: true })];
    servedQueues = { tenantEnabled: true, queues: [aQueue()] };
    servedMemos = [waiting({ deliveryId: "d1" })];
    await bump();
    expect(nextItem()).toBeNull();
  });

  it("shows no badge on a memo that is not waiting, even if the field came along", async () => {
    servedMemos = [aMemo({ id: "a", state: "delivered", runNext: true })];
    await render();
    await click(must(document.querySelector(".memo-sent-fold"), "history fold"));
    expect(document.querySelector(".memo-row")).not.toBeNull();
    expect(badge()).toBeNull();
    servedMemos = [waiting({ runNext: true, deliveryId: "d1" })];
    await bump();
    expect(document.querySelector(".memo-row")).not.toBeNull();
    expect(badge()).toBeNull();
  });
});

describe("Run now for one memo in the memo menu (#4919)", () => {
  const rowMenuBtn = () => must(document.querySelector<HTMLElement>(".memo-row .memo-menu-btn"), "row menu button");
  const runItem = () => document.querySelector<HTMLButtonElement>(".ui-menu .memo-menu-runone");
  const waiting = (over: Partial<Memo> = {}) => aMemo({ id: "a", state: "pending", provenance: "member", ...over });
  const open = async () => {
    await render();
    await click(rowMenuBtn());
  };
  const inAnHour = () => new Date(Date.now() + 3600_000).toISOString();

  for (const lang of ["en", "ja"] as const) {
    it(`starts only that memo (${lang})`, async () => {
      setLocale(lang);
      try {
        servedMemos = [waiting()];
        await open();
        const item = must(runItem(), "run item");
        expect(item.textContent).toContain(t("memo.run_one"));
        expect(item.getAttribute("aria-disabled")).toBeNull();
        await click(item);
        expect(memoQueueFireOne).toHaveBeenCalledWith("c1", "a");
        expect(memoQueueFire).not.toHaveBeenCalled();
        expect(document.body.textContent).toContain(t("memo.d_fire_started"));
        expect(document.querySelector(".ui-menu")).toBeNull();
      } finally {
        setLocale("en");
      }
    });
  }

  it("is absent in a category that is not auto-run, and on a memo that already ran", async () => {
    servedCats = [aCat({})];
    servedQueues = { tenantEnabled: true, queues: [] };
    servedMemos = [waiting()];
    await open();
    expect(runItem()).toBeNull();
    servedCats = [aCat({ queue: true })];
    servedQueues = { tenantEnabled: true, queues: [aQueue()] };
    servedMemos = [waiting({ state: "claimed" })];
    await bump();
    expect(runItem()).toBeNull();
  });

  const disabledFor = async (memo: Memo, why: string, queue: Record<string, unknown> = {}) => {
    servedQueues = { tenantEnabled: true, queues: [aQueue(queue)] };
    servedMemos = [memo];
    await open();
    const item = must(runItem(), "run item");
    // Reachable by keyboard (not natively disabled), says why, and a press sends nothing.
    expect(item.disabled).toBe(false);
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(item.querySelector(".memo-cat-menu-why")?.textContent).toBe(why);
    await click(item);
    expect(memoQueueFireOne).not.toHaveBeenCalled();
    expect(memoQueueFire).not.toHaveBeenCalled();
  };

  it("is disabled, with the reason, on a memo held for approval", async () => {
    await disabledFor(waiting({ state: "held", provenance: "workspace" }), t("memo.run_one_held"));
  });
  it("is disabled while a prerequisite is not finished", async () => {
    await disabledFor(
      waiting({ after: ["x"], afterStatus: [{ id: "x", state: "waiting", reason: "running" }] }),
      t("memo.run_one_prerequisites"),
    );
  });
  it("is disabled until its start time", async () => {
    const at = inAnHour();
    await disabledFor(waiting({ startAfter: at }), t("memo.run_one_scheduled", { at: fmtDateTime(at) }));
  });
  it("is disabled until a hand-back's time", async () => {
    const at = inAnHour();
    await disabledFor(waiting({ notBefore: at }), t("memo.run_one_deferred", { at: fmtDateTime(at) }));
  });
  it("is disabled while the category has no free slot, with the category's own words", async () => {
    await disabledFor(waiting(), t("memo.waiting_no_place"), { fireBlock: { reason: "no_place", detail: { by: "category" } } });
  });
  it("is enabled once a start time has passed", async () => {
    servedMemos = [waiting({ startAfter: "2020-01-01T00:00:00Z" })];
    await open();
    expect(must(runItem(), "run item").getAttribute("aria-disabled")).toBeNull();
  });
  it("says a slot is taken when the Control Plane refuses for one, and opens the history", async () => {
    servedMemos = [waiting()];
    memoQueueFireOne.mockResolvedValueOnce({ error: { code: "memo_queue_no_slot", message: "every slot of this queue is taken (2 of 2)" } });
    await open();
    await click(must(runItem(), "run item"));
    expect(dialog().querySelector(".memo-d-slots-full")).not.toBeNull();
  });
  it("shows the Control Plane's refusal of a memo that cannot run, and reads the list again", async () => {
    servedMemos = [waiting()];
    // The memo was held after the list was drawn: the refusal is the first the Console hears of it.
    memoQueueFireOne.mockImplementationOnce(async () => {
      servedMemos = [waiting({ state: "held", provenance: "workspace" })];
      return { error: { code: "memo_queue_memo_not_ready", message: "x" } };
    });
    await open();
    const reads = memoListCalls;
    await click(must(runItem(), "run item"));
    expect(document.body.textContent).toContain(t("err.memo_queue_memo_not_ready"));
    // The list is read again, so the next menu shows what the memo is now.
    expect(memoListCalls).toBeGreaterThan(reads);
    await click(rowMenuBtn());
    const item = must(runItem(), "run item after the refusal");
    expect(item.getAttribute("aria-disabled")).toBe("true");
    expect(item.textContent).toContain(t("memo.run_one_held"));
  });

  it("is unavailable while a press of the category is in flight, and available again after it", async () => {
    servedMemos = [waiting({ id: "a" }), waiting({ id: "b", position: 1 })];
    let finish: (v: unknown) => void = () => {};
    memoQueueFireOne.mockReturnValueOnce(new Promise((r) => (finish = r)));
    await render();
    const rows = () => [...document.querySelectorAll<HTMLElement>(".memo-row .memo-menu-btn")];
    await click(rows()[0]!);
    await click(must(runItem(), "run item"));
    expect(memoQueueFireOne).toHaveBeenCalledTimes(1);
    // The menu of another memo of the same category, opened while the press runs.
    await click(rows()[1]!);
    const busy = must(runItem(), "run item while busy");
    expect(busy.getAttribute("aria-disabled")).toBe("true");
    expect(busy.textContent).toContain(t("memo.run_one_busy"));
    await click(busy);
    expect(memoQueueFireOne).toHaveBeenCalledTimes(1);
    // The category's own button is off too, and a press of it is not sent.
    await act(async () => finish({ id: "d1" }));
    await settle();
    expect(must(runItem(), "run item after").getAttribute("aria-disabled")).toBeNull();
  });

  it("the category's Run now is unavailable while a press from a memo's menu is in flight", async () => {
    servedMemos = [waiting({ id: "a" })];
    let finish: (v: unknown) => void = () => {};
    memoQueueFireOne.mockReturnValueOnce(new Promise((r) => (finish = r)));
    await open();
    await click(must(runItem(), "run item"));
    expect(memoQueueFireOne).toHaveBeenCalledTimes(1);
    const head = headMenuItem({ cls: "memo-cat-run" });
    expect(head.getAttribute("aria-disabled")).toBe("true");
    await click(head);
    expect(memoQueueFire).not.toHaveBeenCalled();
    await act(async () => finish({ id: "d1" }));
    await settle();
    expect(headMenuItem({ cls: "memo-cat-run" }).getAttribute("aria-disabled")).toBeNull();
  });

  it("a menu press does not start while the category's own Run now is in flight", async () => {
    servedMemos = [waiting({ id: "a" })];
    let finish: (v: unknown) => void = () => {};
    memoQueueFire.mockReturnValueOnce(new Promise((r) => (finish = r)));
    await render();
    await click(headMenuItem({ cls: "memo-cat-run" }));
    expect(memoQueueFire).toHaveBeenCalledTimes(1);
    await click(rowMenuBtn());
    const item = must(runItem(), "run item");
    expect(item.getAttribute("aria-disabled")).toBe("true");
    await click(item);
    expect(memoQueueFireOne).not.toHaveBeenCalled();
    await act(async () => finish({ id: "d1" }));
    await settle();
  });
});
