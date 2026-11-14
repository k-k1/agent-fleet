// Render tests for the memo section's auto-run side (ADR 0137 §10): which categories are
// marked, each message's state and origin, the release card (release and the stale refusal),
// the settings dialog (tenant switch off, validation) and the deliveries with "Run now".
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
let servedCats: MemoCategory[] = [];
let servedQueues: unknown = { tenantEnabled: true, queues: [] };
let queuesFail = false;
let servedDeliveries: unknown = [];

const memoRelease = vi.fn();
const memoQueuePut = vi.fn();
const memoQueueDelete = vi.fn();
const memoQueueFire = vi.fn();
const memoQueueDeliveries = vi.fn(async (_id: string) => servedDeliveries);
const memoQueueList = vi.fn(async () => {
  if (queuesFail) throw new Error("down");
  return servedQueues;
});

vi.mock("./api.ts", () => ({
  memoList: async () => servedMemos,
  memoCategoryList: async () => servedCats,
  memoCreate: async () => ({}),
  memoUpdate: async () => ({}),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: async () => ({}),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: () => memoQueueList(),
  memoRelease: (items: unknown) => memoRelease(items),
  memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body),
  memoQueueDelete: (id: string) => memoQueueDelete(id),
  memoQueueFire: (id: string) => memoQueueFire(id),
  memoQueueDeliveries: (id: string) => memoQueueDeliveries(id),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));
const openSessionChat = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => openSessionChat(n), openSessionTerminal: vi.fn() }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { errText } = await import("../../core/api/client.ts");
const { deliveryStateKey } = await import("./queues.ts");

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
  must(document.querySelector<HTMLElement>(`[aria-label="${label}"]`), `button ${label}`);
const dialog = () => must(document.querySelector<HTMLElement>(".memo-q-modal"), "dialog");
const bump = async () => {
  const { useMemoStore } = await import("./store.ts");
  await act(async () => useMemoStore.getState().bump());
  await settle();
};

beforeEach(() => {
  localStorage.clear();
  servedMemos = [];
  servedCats = [aCat({ queue: true })];
  servedQueues = { tenantEnabled: true, queues: [aQueue()] };
  queuesFail = false;
  servedDeliveries = [];
  for (const m of [memoRelease, memoQueuePut, memoQueueDelete, memoQueueFire, memoQueueDeliveries, memoQueueList, openSessionChat]) m.mockClear();
  memoRelease.mockResolvedValue({ released: [], stale: [] });
  memoQueuePut.mockResolvedValue(aQueue());
  memoQueueDelete.mockResolvedValue({ ok: true });
  memoQueueFire.mockResolvedValue({ id: "d1" });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
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
    expect(badge("inbox-q")?.classList.contains("off")).toBe(false);
    expect(badge("plain")).toBeNull();

    servedQueues = { tenantEnabled: true, queues: [aQueue({ enabled: false })] };
    await bump();
    expect(badge("inbox-q")?.textContent).toContain(t("memo.autorun_badge_off"));
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
    await click(must(card.querySelector("button"), "release button"));
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

  it("says the tenant switch is off instead of offering to save", async () => {
    servedQueues = { tenantEnabled: false, queues: [] };
    servedCats = [aCat({})];
    await render();
    await openSettings();
    expect(dialog().querySelector(".memo-q-notice")?.textContent).toBe(t("memo.q_tenant_off"));
    expect(must(dialog().querySelector<HTMLButtonElement>("button[type=submit]"), "save").disabled).toBe(true);
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
    const enable = [...dialog().querySelectorAll<HTMLInputElement>("input[type=checkbox]")][1];
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
      maxActive: 2,
      batch: 3,
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

  it("fires the queue now, and not when nothing waits", async () => {
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await openDeliveries();
    const fire = must(
      [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("memo.d_fire"))),
      "fire",
    );
    expect(fire.disabled).toBe(false);
    await click(fire);
    expect(memoQueueFire).toHaveBeenCalledWith("c1");
  });

  it("disables Run now with nothing waiting", async () => {
    await render();
    await openDeliveries();
    const fire = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("memo.d_fire")))!;
    expect(fire.disabled).toBe(true);
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
  const outcomes = ["clean", "failed", "aborted", "unknown", "not_sent"];
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

describe("the memo auto-run error codes have a text of their own", () => {
  const codes = [
    "memo_locked", "memo_sending", "memo_in_queue", "memo_queue_category", "memo_inbox_in_queue", "memo_release_stale",
    "memo_too_large", "memo_envelope", "memo_rate_limited", "memo_queue_no_instruction", "memo_queue_bad_settings",
    "memo_queues_disabled", "memo_queue_nothing_pending", "memo_queue_no_slot", "memo_queue_member_limit", "memo_queue_bad_repo",
  ];
  it("answers the catalogue line, never the server's message", () => {
    for (const code of codes) expect(errText({ code, message: "SERVER-MESSAGE" }), code).not.toBe("SERVER-MESSAGE");
  });
});
