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
const memoQueueResume = vi.fn();
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
  memoQueueResume: (id: string) => memoQueueResume(id),
  memoQueueDeliveries: (id: string) => memoQueueDeliveries(id),
  memoTidyRuns: async () => ({ runs: [], usedToday: 0, dailyCap: 10 }),
  memoTidyUndo: async () => ({}),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));
const openSessionChat = vi.fn();
vi.mock("../sessions/open.ts", () => ({ openSessionChat: (n: string) => openSessionChat(n), openSessionTerminal: vi.fn() }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
const { fmtDateTime } = await import("../../lib/intl.ts");
const { errText } = await import("../../core/api/client.ts");
const { deliveryStateKey } = await import("./queues.ts");
const { resetShownNotes } = await import("./shownNotes.ts");

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
  must(document.querySelector<HTMLElement>(`[aria-label="${label}"]`), `button ${label}`);
const dialog = () => must(document.querySelector<HTMLElement>(".memo-q-modal"), "dialog");
const bump = async () => {
  const { useMemoStore } = await import("./store.ts");
  await act(async () => useMemoStore.getState().bump());
  await settle();
};

beforeEach(() => {
  resetShownNotes();
  localStorage.clear();
  servedMemos = [];
  servedCats = [aCat({ queue: true })];
  servedQueues = { tenantEnabled: true, queues: [aQueue()] };
  queuesFail = false;
  servedDeliveries = [];
  for (const m of [memoRelease, memoQueuePut, memoQueueDelete, memoQueueFire, memoQueueResume, memoQueueDeliveries, memoQueueList, openSessionChat]) m.mockClear();
  memoRelease.mockResolvedValue({ released: [], stale: [] });
  memoQueuePut.mockResolvedValue(aQueue());
  memoQueueDelete.mockResolvedValue({ ok: true });
  memoQueueFire.mockResolvedValue({ id: "d1" });
  memoQueueResume.mockResolvedValue({});
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
    expect(origins[2]!.querySelector("button")).toBeNull(); // deleted session
    const title = must(document.querySelector(".memo-release-title button"), "card link");
    await click(title);
    expect(openSessionChat).toHaveBeenCalledWith("worker-1");
    useSessionsStore.setState({ sessions: [] });
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

  it("is also said in the run history, with the same button, and Run now still works", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ paused: true })] };
    servedMemos = [aMemo({ id: "a", state: "pending", provenance: "member" })];
    await render();
    await click(byLabel(t("memo.autorun_deliveries")));
    expect(dialog().querySelector(".memo-q-paused")?.textContent).toContain(t("memo.paused_notice"));
    const fire = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("memo.d_fire")))!;
    expect(fire.disabled).toBe(false);
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
    const fire = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("memo.d_fire")))!;
    expect(fire.disabled).toBe(false);
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
    sentAt: "", closedAt: "", heldBy: "", heldSession: "", heldSeenAt: "", overdue: false, overdueAt: "", auto: false, retire: "",
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
    await click(byLabel(t("memo.autorun_deliveries")));
    const fire = [...dialog().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t("memo.d_fire")))!;
    await click(fire);
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
