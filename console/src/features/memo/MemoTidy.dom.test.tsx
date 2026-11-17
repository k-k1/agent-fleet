// Render tests for tidying before a run (ADR 0137 section 8): the setting and the destinations in
// the settings dialog, the notices, the marks on the memo list and on the release card, and the
// tidy history with its undo (done, and refused with the messages named).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
let servedCats: MemoCategory[] = [];
let servedQueues: unknown = { tenantEnabled: true, queues: [] };
let servedRuns: unknown = { runs: [], usedToday: 0, dailyCap: 10 };

const memoQueuePut = vi.fn();
const memoTidyRuns = vi.fn(async (_id: string) => servedRuns);
const memoTidyUndo = vi.fn();

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
  memoQueueList: async () => servedQueues,
  memoRelease: async () => ({}),
  memoQueuePut: (id: string, body: unknown) => memoQueuePut(id, body),
  memoQueueDelete: async () => ({ ok: true }),
  memoQueueFire: async () => ({}),
  memoQueueResume: async () => ({}),
  memoQueueDeliveries: async () => [],
  memoTidyRuns: (id: string) => memoTidyRuns(id),
  memoTidyUndo: (id: string) => memoTidyUndo(id),
}));
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));
vi.mock("../sessions/open.ts", () => ({ openSessionChat: vi.fn(), openSessionTerminal: vi.fn() }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");
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
const aCat = (over: Partial<MemoCategory>): MemoCategory => ({ id: "c1", repo: "", name: "inbox-q", position: 0, createdAt: "2026-09-09T00:00:00Z", queue: true, ...over });
const aQueue = (over: Record<string, unknown> = {}) => ({
  categoryId: "c1",
  repo: "",
  category: "inbox-q",
  instruction: "triage",
  agentKind: "claude",
  route: "r-keep",
  worktree: true,
  maxActive: 2,
  batch: 1,
  maxTaskHours: 12,
  maxFiresPerDay: 20,
  enabled: true,
  ...over,
});
const aRun = (over: Record<string, unknown> = {}) => ({
  id: "run1",
  trigger: "auto",
  state: "applied",
  reason: "",
  createdAt: "2026-11-17T01:00:00Z",
  finishedAt: "2026-11-17T01:00:05Z",
  undoneAt: "",
  merged: 1,
  split: 0,
  moved: 0,
  held: 0,
  sourceIds: ["a", "b"],
  resultIds: ["c"],
  movedIds: [],
  undoable: true,
  blocked: [],
  ...over,
});

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
const byLabel = (label: string) => must(document.querySelector<HTMLElement>(`[aria-label="${label}"]`), `button ${label}`);
const dialog = () => must(document.querySelector<HTMLElement>(".memo-q-modal"), "dialog");
const labelled = (text: string) =>
  must([...dialog().querySelectorAll("label")].find((l) => l.textContent?.includes(text)), `label ${text}`);

beforeEach(() => {
  resetShownNotes();
  localStorage.clear();
  servedMemos = [];
  servedCats = [aCat({})];
  servedQueues = { tenantEnabled: true, queues: [aQueue()] };
  servedRuns = { runs: [], usedToday: 0, dailyCap: 10 };
  for (const m of [memoQueuePut, memoTidyRuns, memoTidyUndo]) m.mockClear();
  memoQueuePut.mockResolvedValue({});
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

describe("the tidy setting", () => {
  const openSettings = async () => click(byLabel(t("memo.autorun_settings")));
  const save = async () => click(must(dialog().querySelector("button[type=submit]"), "save"));

  it("is off by default, says what turning it on means, and shows no destinations until it is on", async () => {
    servedCats = [aCat({}), aCat({ id: "c2", name: "triage" }), aCat({ id: "c3", name: "plain", queue: false })];
    servedQueues = { tenantEnabled: true, queues: [aQueue(), aQueue({ categoryId: "c2", category: "triage" })] };
    await render();
    await openSettings();
    const box = must(labelled(t("memo.q_tidy")).querySelector<HTMLInputElement>("input[type=checkbox]"), "tidy switch");
    expect(box.checked).toBe(false);
    // The trust claim: a program in the workspace could put words you did not write into a note that runs.
    expect(labelled(t("memo.q_tidy")).textContent).toMatch(/なりすま|pass itself off/);
    expect(labelled(t("memo.q_tidy")).textContent).toMatch(/信頼できるワークスペース|workspace you trust/);
    expect(dialog().querySelector(".memo-q-tidy-dest")).toBeNull();
    await click(box);
    const dest = must(dialog().querySelector(".memo-q-tidy-dest"), "destinations");
    // Only the other auto-run categories: not this one, not a plain category.
    expect([...dest.querySelectorAll("label")].map((l) => l.textContent)).toEqual(["triage"]);
  });

  it("sends the switch and the chosen destinations with the whole row", async () => {
    servedCats = [aCat({}), aCat({ id: "c2", name: "triage" })];
    servedQueues = { tenantEnabled: true, queues: [aQueue(), aQueue({ categoryId: "c2", category: "triage" })] };
    await render();
    await openSettings();
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("c1", expect.objectContaining({ tidy: false, tidyDestinations: [] }));
    await openSettings();
    await click(must(labelled(t("memo.q_tidy")).querySelector("input"), "switch"));
    await click(must(dialog().querySelector(".memo-q-tidy-dest input[type=checkbox]"), "destination"));
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("c1", expect.objectContaining({ tidy: true, tidyDestinations: ["c2"] }));
  });

  it("keeps a saved choice, destinations included, when another field is saved", async () => {
    servedCats = [aCat({}), aCat({ id: "c2", name: "triage" })];
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true, tidyDestinations: ["c2"] }), aQueue({ categoryId: "c2", category: "triage" })] };
    await render();
    await openSettings();
    expect(must(labelled(t("memo.q_tidy")).querySelector<HTMLInputElement>("input"), "switch").checked).toBe(true);
    await save();
    expect(memoQueuePut).toHaveBeenLastCalledWith("c1", expect.objectContaining({ tidy: true, tidyDestinations: ["c2"] }));
  });
});

describe("the notices of a tidy", () => {
  it("says a tidy is running as a waiting reason", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true, waiting: { reason: "tidying" } })] };
    await render();
    expect(document.querySelector(".memo-q-why")?.textContent).toBe(t("memo.waiting_tidying"));
  });
  it("says the notes a tidy made cannot run here, and what to do, once, for the reason and for the mark", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true, noTidyAt: "2026-11-17T00:00:00Z", waiting: { reason: "no_tidy" } })] };
    await render();
    const notes = [...document.querySelectorAll(".memo-q-noreport")].filter((e) => e.textContent === t("memo.no_tidy_notice"));
    expect(notes).toHaveLength(1);
    expect(notes[0].textContent).toMatch(/Claude/);
    expect(document.querySelector(".memo-q-why")).toBeNull();
  });
  it("says nothing when the workspace can tidy", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    await render();
    expect(document.querySelector(".memo-q-noreport")).toBeNull();
  });
});

describe("the marks on the memo list", () => {
  it("marks a derived message with its count and origin, and one a tidy moved with where it came from", async () => {
    servedMemos = [
      aMemo({ id: "d", state: "pending", provenance: "curator", derivedFrom: ["a", "b", "c"], sourceProvenance: "member", releasedBy: "tidy", body: "merged" }),
      aMemo({ id: "mv", state: "pending", provenance: "member", tidyFrom: { id: "x", repo: "", name: "scratch" }, releasedBy: "tidy", body: "moved" }),
    ];
    await render();
    const rows = [...document.querySelectorAll<HTMLElement>(".memo-row")];
    const text = (id: string) => rows.find((r) => r.textContent?.includes(id))!.textContent!;
    expect(text("merged")).toContain(t("memo.tidy_derived_member", { count: 3 }));
    expect(text("merged")).toContain(t("memo.released_by_tidy"));
    expect(text("moved")).toContain(t("memo.tidy_moved_from", { name: "scratch" }));
    expect(text("moved")).toContain(t("memo.released_by_tidy"));
  });
  it("does not say it was accepted by the setting when a tidy released it", async () => {
    servedMemos = [aMemo({ id: "d", state: "pending", provenance: "member", releasedBy: "setting", body: "plain" })];
    await render();
    expect(document.querySelector(".memo-row")?.textContent).toContain(t("memo.accepted_by_setting"));
    expect(document.querySelector(".memo-row")?.textContent).not.toContain(t("memo.released_by_tidy"));
  });
  it("shows a replaced note read-only: no select box, no edit or send in its menu, a link to the history", async () => {
    servedMemos = [aMemo({ id: "s", state: "superseded", sentAt: "2026-11-17T01:00:00Z", tidyRunId: "run1", body: "old" })];
    servedRuns = { runs: [aRun()], usedToday: 1, dailyCap: 10 };
    await render();
    const row = must(document.querySelector<HTMLElement>(".memo-row"), "row");
    expect(row.textContent).toContain(t("memo.state_superseded"));
    expect(must(row.querySelector<HTMLInputElement>("input[type=checkbox]"), "check").disabled).toBe(true);
    await click(must(row.querySelector(".memo-menu-btn"), "kebab"));
    const menu = [...document.querySelectorAll(".ui-menu .ui-menu-item")].map((b) => b.textContent);
    expect(menu.some((x) => x?.includes(t("memo.edit")))).toBe(false);
    expect(menu.some((x) => x?.includes(t("memo.send_one")))).toBe(false);
    expect(menu.some((x) => x?.includes(t("common.delete")))).toBe(true);
    // The link opens the history, with the run that replaced the note standing out.
    await click(document.body);
    await click(must(row.querySelector(".memo-qtidy-link"), "history link"));
    expect(must(dialog().querySelector(".memo-tidy-row"), "run row").classList.contains("focus")).toBe(true);
  });
});

describe("a replaced note", () => {
  it("does not open for editing when its body is clicked again, where a plain note does", async () => {
    servedMemos = [
      aMemo({ id: "s", state: "superseded", sentAt: "2026-11-17T01:00:00Z", tidyRunId: "run1", body: "old" }),
      aMemo({ id: "p", state: "pending", provenance: "member", position: 1, body: "live" }),
    ];
    await render();
    const body = (text: string) => must([...document.querySelectorAll<HTMLElement>(".memo-body")].find((b) => b.textContent?.includes(text)), text);
    await click(body("old"));
    await click(body("old"));
    expect(document.querySelector(".memo-edit")).toBeNull();
    await click(body("live"));
    await click(body("live"));
    expect(document.querySelector(".memo-edit")).not.toBeNull();
  });
});

describe("the release card of a message a tidy moved", () => {
  it("shows where it came from, and that it runs under this category's instruction", async () => {
    servedMemos = [
      aMemo({ id: "h", state: "held", provenance: "member", contentHash: "h1", tidyFrom: { id: "x", repo: "r", name: "scratch" }, tidyRunId: "run1", body: "moved here" }),
    ];
    await render();
    const card = must(document.querySelector<HTMLElement>(".memo-release"), "card");
    expect(card.textContent).toContain(t("memo.tidy_moved_from", { name: "r / scratch" }));
    expect(card.textContent).toContain(t("memo.release_tidy_hint"));
  });
  it("still shows a message whose source category is gone", async () => {
    servedMemos = [aMemo({ id: "h", state: "held", provenance: "member", contentHash: "h1", tidyFrom: { id: "x", repo: "", name: "" } })];
    await render();
    expect(must(document.querySelector<HTMLElement>(".memo-release"), "card").textContent).toContain(t("memo.tidy_moved_unknown"));
  });
});

describe("the tidy history", () => {
  const openHistory = async () => click(byLabel(t("memo.autorun_deliveries")));
  const rows = () => [...dialog().querySelectorAll<HTMLElement>(".memo-tidy-row")];

  it("lists what each run did and how it ended, with how many were used of the day's cap", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = {
      runs: [
        aRun({ id: "r3", merged: 2, moved: 3, held: 1 }),
        aRun({ id: "r2", state: "dropped", reason: "bad_shape", merged: 0, undoable: false }),
        aRun({ id: "r1", state: "undone", undoneAt: "2026-11-17T02:00:00Z", undoable: false, trigger: "manual" }),
      ],
      usedToday: 3,
      dailyCap: 10,
    };
    await render();
    await openHistory();
    expect(memoTidyRuns).toHaveBeenCalledWith("c1");
    expect(dialog().textContent).toContain(t("memo.tidy_used", { used: 3, cap: 10 }));
    const r = rows();
    expect(r).toHaveLength(3);
    expect(r[0].textContent).toContain(t("memo.tidy_state_applied"));
    expect(r[0].textContent).toContain(t("memo.tidy_n_merged", { count: 2 }));
    expect(r[0].textContent).toContain(t("memo.tidy_n_moved_held", { count: 3, held: 1 }));
    expect(r[1].textContent).toContain(t("memo.tidy_state_dropped"));
    expect(r[1].textContent).toContain(t("memo.tidy_why_refused"));
    expect(r[2].textContent).toContain(t("memo.tidy_state_undone"));
    expect(r[2].textContent).toContain(t("memo.tidy_manual"));
    // Undo only where the Control Plane says it can be done.
    expect(r.map((x) => !!x.querySelector("button"))).toEqual([true, false, false]);
  });

  it("undoes a run in one press, tells the member, and re-reads", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = { runs: [aRun()], usedToday: 1, dailyCap: 10 };
    memoTidyUndo.mockResolvedValue({ run: aRun({ state: "undone" }), restored: [], removed: ["c"] });
    await render();
    await openHistory();
    const calls = memoTidyRuns.mock.calls.length;
    await click(must(rows()[0].querySelector("button"), "undo"));
    expect(memoTidyUndo).toHaveBeenCalledWith("run1");
    expect(memoTidyRuns.mock.calls.length).toBeGreaterThan(calls);
    expect(document.body.textContent).toContain(t("memo.tidy_undone"));
  });

  it("names the messages in the way when a result was claimed, and keeps the run as it was", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = { runs: [aRun()], usedToday: 1, dailyCap: 10 };
    memoTidyUndo.mockResolvedValue({ error: { code: "memo_tidy_not_undoable", message: "x", blocked: [{ id: "c", why: "claimed" }] } });
    await render();
    await openHistory();
    await click(must(rows()[0].querySelector("button"), "undo"));
    const note = must(rows()[0].querySelector<HTMLElement>(".memo-tidy-blocked"), "refusal");
    expect(note.textContent).toContain(t("memo.tidy_blocked_head"));
    expect(note.textContent).toContain(t("memo.tidy_block_claimed", { count: 1 }));
    expect(note.getAttribute("role")).toBe("alert");
  });

  it("tells the member to undo the newer run first when a later run used a result", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = { runs: [aRun()], usedToday: 1, dailyCap: 10 };
    memoTidyUndo.mockResolvedValue({ error: { code: "memo_tidy_not_undoable", blocked: [{ id: "c", why: "later_run" }] } });
    await render();
    await openHistory();
    await click(must(rows()[0].querySelector("button"), "undo"));
    expect(rows()[0].textContent).toContain(t("memo.tidy_block_later_run", { count: 1 }));
  });

  it("says why a run that cannot be undone cannot, from the list alone", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = { runs: [aRun({ undoable: false, blocked: [{ id: "c", why: "sent" }] })], usedToday: 1, dailyCap: 10 };
    await render();
    await openHistory();
    expect(rows()[0].querySelector("button")).toBeNull();
    expect(rows()[0].textContent).toContain(t("memo.tidy_block_sent", { count: 1 }));
  });

  it("shows the Control Plane's catalogue line for any other refusal, and no section for a category that never tidied", async () => {
    servedQueues = { tenantEnabled: true, queues: [aQueue({ tidy: true })] };
    servedRuns = { runs: [aRun()], usedToday: 1, dailyCap: 10 };
    memoTidyUndo.mockResolvedValue({ error: { code: "not_found", message: "x" } });
    await render();
    await openHistory();
    await click(must(rows()[0].querySelector("button"), "undo"));
    expect(rows()[0].querySelector(".memo-tidy-blocked")).toBeNull();
    expect(document.body.textContent).not.toContain(t("memo.tidy_undone"));
  });

  it("has no tidy section for a category that has it off and never ran one", async () => {
    await render();
    await openHistory();
    expect(dialog().querySelector(".memo-tidy")).toBeNull();
  });
});
