// A note that waits for other notes of its auto-run category (ADR 0137, addendum 2026-11-19): what
// its row says it waits for, why it came back for approval, and choosing what it waits for in the
// edit view, sent only when the member changed it.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory } from "../../types/memo.ts";
import type { WorkItem } from "../workitems/read.ts";

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
const { useWorkItemStore } = await import("../workitems/store.ts");
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
  useWorkItemStore.setState({ payload: null, loaded: false });
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
  // The editor takes the focus for its text area on the next frame; let that pass.
  await frame();
};
const editRow = () => document.querySelector<HTMLElement>(".memo-row.editing")!;
const frame = () => act(async () => void (await new Promise((r) => requestAnimationFrame(() => r(null)))));

// The prerequisite picker: chips for the chosen memos, a search field, a list of matches.
const search = () => editRow().querySelector<HTMLInputElement>(".memo-after-search")!;
const chips = () => [...editRow().querySelectorAll<HTMLElement>(".memo-after-chip")];
const opts = () => [...editRow().querySelectorAll<HTMLElement>(".memo-after-opt")];
const titles = (els: HTMLElement[]) => els.map((e) => e.querySelector(".memo-after-title")?.textContent ?? "");
const focusSearch = async () => {
  await act(async () => void search().focus());
  await settle();
  expect(document.activeElement).toBe(search());
};
const typeSearch = async (v: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(search(), v);
    search().dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
};
const pressKey = async (key: string) => {
  const ev = new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true });
  await act(async () => void search().dispatchEvent(ev));
  await settle();
  return ev;
};
const removeChip = (i: number) => click(chips()[i].querySelector("button")!);
const bump = async () => {
  const { useMemoStore } = await import("./store.ts");
  await act(async () => useMemoStore.getState().bump());
  await settle();
};
const workItem = (key: string, title: string): WorkItem =>
  ({ id: key, queryId: "q", provider: "github", kind: "issue", key, title, state: "open", url: "", assignee: "", labels: [], labelColors: {}, repo: "", updatedAt: "", checks: { state: "", total: 0, failed: 0, pending: 0 }, mergeable: "" }) as WorkItem;

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

  it("offers the other notes of the category, earlier and later, and sends after only when the member changed it", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "p1", body: "first job", state: "pending", position: 0 }),
      aMemo({ id: "p2", body: "other job", state: "delivered", position: 1 }),
      aMemo({ id: "d1", body: "third job", state: "pending", position: 2, createdAt: "2026-09-09T00:00:01Z", after: ["p2"] }),
      aMemo({ id: "z9", body: "later job", state: "pending", position: 3, createdAt: "2026-09-09T00:00:02Z" }),
      // Waits for the note being edited through z8, so picking it would make the two wait for each other.
      aMemo({ id: "z7", body: "loop job", state: "pending", position: 4, createdAt: "2026-09-09T00:00:03Z", after: ["z8"] }),
      aMemo({ id: "z8", body: "chained job", state: "pending", position: 5, createdAt: "2026-09-09T00:00:04Z", after: ["d1"] }),
    ];
    await renderSection();
    await openEditor("third job");
    expect(editRow().textContent).toContain(t("memo.after_edit_label"));
    // The one it waits for is a chip; the rest are offered when the field is focused, the nearest
    // (latest in the category's order) first. Never itself, nor one that already waits for it.
    expect(titles(chips())).toEqual(["other job"]);
    expect(opts()).toHaveLength(0);
    await focusSearch();
    expect(titles(opts())).toEqual(["later job", "first job"]);
    // A text edit alone leaves it out of the patch.
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "third job", category: "inbox" });

    await openEditor("third job");
    await focusSearch();
    await click(opts()[1]);
    // Added, it leaves the list and joins the chips in the category's order.
    expect(titles(chips())).toEqual(["first job", "other job"]);
    expect(titles(opts())).toEqual(["later job"]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "third job", category: "inbox", after: ["p1", "p2"] });

    await openEditor("third job");
    await removeChip(0);
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
    expect(chips().map((c) => c.textContent)).toEqual(["acme/app#3" + t("memo.after_r_deleted")]);
    // It is never offered as a candidate, and adding another keeps it.
    await focusSearch();
    expect(titles(opts())).toEqual(["first job"]);
    await click(opts()[0]);
    expect(chips().map((c) => c.textContent?.includes("acme/app#3"))).toEqual([false, true]);
    // The × removes it.
    await removeChip(1);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "second job", category: "inbox", after: ["p1"] });
  });

  it("names a chosen memo the list no longer offers by its own text when it is still loaded", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "s1", body: "replaced job\nwith more", state: "superseded", position: 0, reference: { provider: "github", key: "acme/app#9", url: "" } }),
      aMemo({ id: "d1", body: "second job", state: "held", position: 1, createdAt: "2026-09-09T00:00:01Z", after: ["s1"] }),
    ];
    await renderSection();
    await openEditor("second job");
    expect(chips()).toHaveLength(1);
    expect(chips()[0].querySelector(".memo-after-key")!.textContent).toBe("acme/app#9");
    expect(chips()[0].querySelector(".memo-after-title")!.textContent).toBe("replaced job");
    expect(chips()[0].title).toBe("acme/app#9\nreplaced job\nwith more");
  });

  it("sends a later note as a prerequisite", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "d1", body: "first job", state: "held", position: 0 }),
      aMemo({ id: "z9", body: "continuation job", state: "pending", position: 1, createdAt: "2026-09-09T00:00:02Z" }),
    ];
    await renderSection();
    await openEditor("first job");
    await focusSearch();
    await click(opts()[0]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("d1", { body: "first job", category: "inbox", after: ["z9"] });
  });

  it("says a run without a pull request waits for a Done: answer", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({
        id: "d1",
        body: "second job",
        state: "pending",
        after: ["p1"],
        afterStatus: [{ id: "p1", label: "acme/app#7", state: "waiting", reason: "answer" }],
      }),
    ];
    await renderSection();
    expect(rowOf("second job").querySelector(".memo-after")!.textContent).toContain("acme/app#7: " + t("memo.after_r_answer"));
  });

  it("says a run that ended without a pull request and without Done: held it, with what that run said", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({
        id: "d1",
        body: "second job",
        state: "held",
        after: ["p1"],
        afterStatus: [{ id: "p1", label: "acme/app#7", state: "failed", reason: "not_done", answer: "Stopped at part 3: #7" }],
        heldReason: { after: "p1", reason: "not_done" },
      }),
    ];
    await renderSection();
    const row = rowOf("second job");
    expect(row.querySelector(".memo-after-held")?.textContent).toBe(
      t("memo.after_held", { label: "acme/app#7", reason: t("memo.after_r_not_done") }),
    );
    expect(row.querySelector(".memo-after")!.textContent).toContain(
      "acme/app#7: " + t("memo.after_r_not_done_named", { answer: "Stopped at part 3: #7" }),
    );
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
    await focusSearch();
    await click(opts()[0]);
    await click(btn(editRow(), t("memo.save")));
    expect(document.body.textContent).toContain(t("err.memo_after_invalid"));
  });
});

describe("the editor's text area", () => {
  it("is sized to a long note as it opens, not left at its rows height until the next keystroke", async () => {
    const spy = vi.spyOn(HTMLTextAreaElement.prototype, "scrollHeight", "get").mockImplementation(() => 137);
    try {
      servedCats = [queueCat];
      servedMemos = [aMemo({ id: "me", body: "line one\nline two\nline three\nline four\nline five", state: "held" })];
      await renderSection();
      await openEditor("line one");
      expect(editRow().querySelector<HTMLTextAreaElement>(".memo-edit-text")!.style.height).toBe("137px");
    } finally {
      spy.mockRestore();
    }
  });
});

describe("choosing what a note waits for by search", () => {
  const ref = (key: string) => ({ provider: "github", key, url: "" });
  const seed = () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "a1", body: "ship the importer", state: "delivered", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: "write the docs for it", state: "pending", position: 1, createdAt: "2026-09-09T00:00:01Z", reference: ref("acme/app#8") }),
      aMemo({ id: "a3", body: "rotate the keys", state: "held", position: 2, createdAt: "2026-09-09T00:00:02Z" }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 3, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    useWorkItemStore.setState({
      payload: { items: [workItem("acme/app#7", "Crash when saving"), workItem("acme/app#8", "Importer docs")], queries: [], sessions: [], fetchedAt: "", running: false } as never,
      loaded: true,
    });
  };

  it("filters by key, by the work item's title and by the memo's text, and says what it matched", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    // Focused with nothing typed: everything on offer, the nearest first.
    expect(titles(opts())).toEqual(["rotate the keys", "Importer docs", "Crash when saving"]);
    await typeSearch("app#7");
    expect(titles(opts())).toEqual(["Crash when saving"]);
    await typeSearch("IMPORTER");
    // The title of #8, and the text of #7 ("ship the importer"): both match, the nearest first.
    expect(titles(opts())).toEqual(["Importer docs", "Crash when saving"]);
    await typeSearch("keys rotate");
    expect(titles(opts())).toEqual(["rotate the keys"]);
    await typeSearch("zzz");
    expect(opts()).toHaveLength(0);
    expect(editRow().textContent).toContain(t("memo.after_no_match"));
  });

  it("offers at most eight and says how many more there are", async () => {
    servedCats = [queueCat];
    servedMemos = [
      ...Array.from({ length: 11 }, (_, i) => aMemo({ id: "n" + i, body: "job " + i, state: "pending", position: i, createdAt: "2026-09-09T00:00:" + String(i).padStart(2, "0") + "Z" })),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 20, createdAt: "2026-09-09T00:01:00Z" }),
    ];
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    expect(opts()).toHaveLength(8);
    expect(editRow().querySelector(".memo-after-more")!.textContent).toBe(t("memo.after_more", { n: 3 }));
    await typeSearch("job 1");
    // "job 1", "job 10": both fit.
    expect(titles(opts())).toEqual(["job 10", "job 1"]);
    expect(editRow().querySelector(".memo-after-more")).toBeNull();
  });

  it("tells two memos of the same issue apart in the list and in the chips", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "a1", body: "part one", state: "delivered", position: 0, createdAt: "2026-09-01T08:00:00Z", reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: "part two", state: "pending", position: 1, createdAt: "2026-09-02T09:30:00Z", releasedBy: "continuation", chainSeq: 2, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 2, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    useWorkItemStore.setState({
      payload: { items: [workItem("acme/app#7", "Crash when saving")], queries: [], sessions: [], fetchedAt: "", running: false } as never,
      loaded: true,
    });
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    const names = opts().map((o) => o.textContent!);
    expect(new Set(names).size).toBe(2);
    // Both read "acme/app#7 Crash when saving"; the rest tells them apart.
    expect(names[0]).toContain("acme/app#7Crash when saving");
    expect(names[0]).toContain(t("memo.state_pending"));
    expect(names[0]).toContain(t("memo.after_part", { seq: 2 }));
    expect(names[1]).toContain(t("memo.state_delivered"));
    expect(names[1]).not.toContain(t("memo.after_part", { seq: 2 }));
    expect(opts()[0].title).toBe("acme/app#7\npart two");
    await click(opts()[0]);
    await click(opts()[0]);
    const chipNames = chips().map((c) => c.textContent!);
    expect(new Set(chipNames).size).toBe(2);
    expect(chipNames[0]).toContain(t("memo.state_delivered"));
    expect(chipNames[1]).toContain(t("memo.after_part", { seq: 2 }));
  });

  it("adds with the keyboard, and Escape closes the list without leaving the editor", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    expect(opts().map((o) => o.getAttribute("aria-selected"))).toEqual(["true", "false", "false"]);
    await pressKey("ArrowDown");
    await pressKey("ArrowDown");
    expect(opts().map((o) => o.getAttribute("aria-selected"))).toEqual(["false", "false", "true"]);
    expect(search().getAttribute("aria-activedescendant")).toBe(opts()[2].id);
    await pressKey("ArrowDown"); // wraps
    expect(opts()[0].getAttribute("aria-selected")).toBe("true");
    await pressKey("ArrowUp"); // wraps back
    expect(opts()[2].getAttribute("aria-selected")).toBe("true");
    await pressKey("Escape");
    expect(opts()).toHaveLength(0);
    expect(editRow()).toBeTruthy();
    // ArrowDown opens it again; Enter adds the active one, and is not taken as a save.
    await pressKey("ArrowDown");
    expect(opts()).toHaveLength(3);
    const enter = await pressKey("Enter");
    expect(enter.defaultPrevented).toBe(true);
    expect(titles(chips())).toEqual(["Crash when saving"]);
    expect(memoUpdate).not.toHaveBeenCalled();
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a1"] });
  });

  it("Escape closes the list first, and the next one cancels the edit", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await pressKey("Escape");
    expect(opts()).toHaveLength(0);
    expect(document.querySelector(".memo-row.editing")).not.toBeNull();
    await pressKey("Escape");
    expect(document.querySelector(".memo-row.editing")).toBeNull();
    expect(memoUpdate).not.toHaveBeenCalled();
  });

  it("keeps the active option in view and wires the combobox only while the list is there", async () => {
    seed();
    const scrolled: (string | null)[] = [];
    const orig = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function (this: Element) {
      scrolled.push(this.getAttribute("aria-selected") === "true" ? this.id : null);
    };
    try {
      await renderSection();
      await openEditor("the edited one");
      expect(search().getAttribute("aria-controls")).toBeNull();
      await focusSearch();
      const list = editRow().querySelector("[role=listbox]")!;
      expect(search().getAttribute("aria-controls")).toBe(list.id);
      // Only options inside the listbox.
      expect([...list.children].every((c) => c.getAttribute("role") === "option")).toBe(true);
      scrolled.length = 0;
      await pressKey("ArrowUp"); // wraps to the last
      expect(scrolled).toEqual([opts()[2].id]);
      expect(search().getAttribute("aria-activedescendant")).toBe(opts()[2].id);
    } finally {
      Element.prototype.scrollIntoView = orig;
    }
  });

  it("does not take the focus back from the search field when the editor's first frame comes later", async () => {
    seed();
    await renderSection();
    // Hold every frame callback, open the editor, move to the search field, then let the frames run.
    const queued: FrameRequestCallback[] = [];
    const raf = vi.spyOn(window, "requestAnimationFrame").mockImplementation((cb) => (queued.push(cb), queued.length));
    try {
      for (let i = 0; i < 2 && !document.querySelector(".memo-row.editing"); i++) await click(rowOf("the edited one").querySelector<HTMLElement>(".memo-body")!);
      expect(queued.length).toBeGreaterThan(0);
      await act(async () => void search().focus());
      await act(async () => void queued.splice(0).forEach((cb) => cb(0)));
    } finally {
      raf.mockRestore();
    }
    expect(document.activeElement).toBe(search());
    expect(opts()).toHaveLength(3);
  });

  const chooseThenLose = async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await click(opts()[2]); // a1, the oldest
    expect(titles(chips())).toEqual(["Crash when saving"]);
    // The poll brings a1 moved to another category: it is no longer a candidate.
    servedMemos = servedMemos.map((x) => (x.id === "a1" ? { ...x, category: "elsewhere" } : x));
    await bump();
    expect(chips()).toHaveLength(1);
  };

  it("keeps a chosen memo that stops being a candidate while the editor is open, in the chips and in the save", async () => {
    await chooseThenLose();
    // Adding another keeps it; one no longer offered reads after the offered ones.
    await focusSearch();
    expect(titles(opts())).toEqual(["rotate the keys", "Importer docs"]);
    await click(opts()[1]);
    expect(chips()).toHaveLength(2);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a2", "a1"] });
  });

  it("does not offer a memo that left again once its chip is removed, even with no reason given", async () => {
    await chooseThenLose();
    await removeChip(0);
    await focusSearch();
    expect(titles(opts())).toEqual(["rotate the keys", "Importer docs"]);
    await click(btn(editRow(), t("memo.save")));
    // Chosen at the start of the edit: none; none now.
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox" });
  });

  it("sends a changed choice in the category's order, and nothing when the set is as it was", async () => {
    seed();
    servedMemos = servedMemos.map((x) => (x.id === "me" ? { ...x, after: ["a3", "a2", "a1"] } : x));
    await renderSection();
    await openEditor("the edited one");
    // The chips read in the category's order, whatever order was stored.
    expect(chips().map((c) => c.querySelector(".memo-after-title")!.textContent)).toEqual(["Crash when saving", "Importer docs", "rotate the keys"]);
    // Taking one off and putting it back is no change: nothing is sent.
    await removeChip(1);
    await focusSearch();
    await click(opts()[0]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox" });
    // A real change is sent in the category's order.
    await openEditor("the edited one");
    await removeChip(1);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a1", "a3"] });
  });

  it("does not send what another device changed meanwhile when only the text was edited", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    servedMemos = servedMemos.map((x) => (x.id === "me" ? { ...x, after: ["a1"] } : x));
    await bump();
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox" });
  });

  it("tells apart two memos of one issue that read the same, by the start of their text", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "a1", body: "first attempt: retry the import", state: "pending", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: "second attempt: skip bad rows", state: "pending", position: 1, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 2, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    servedMemos[1].createdAt = servedMemos[0].createdAt;
    useWorkItemStore.setState({
      payload: { items: [workItem("acme/app#7", "Crash when saving")], queries: [], sessions: [], fetchedAt: "", running: false } as never,
      loaded: true,
    });
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    const names = opts().map((o) => o.textContent!);
    expect(new Set(names).size).toBe(2);
    expect(names[0]).toContain("second attempt: skip bad rows");
    expect(names[1]).toContain("first attempt: retry the import");
    expect(new Set(opts().map((o) => o.getAttribute("aria-label"))).size).toBe(2);
    await click(opts()[0]);
    await click(opts()[0]);
    const labels = chips().map((c) => c.querySelector("button")!.getAttribute("aria-label"));
    expect(new Set(labels).size).toBe(2);
    expect(labels[0]).toContain("first attempt");
  });

  it("sends the choice in the category's order as it is at the save, when two notes made in one second swap places meanwhile", async () => {
    servedCats = [queueCat];
    const sameSecond = "2026-09-09T00:00:01Z";
    servedMemos = [
      aMemo({ id: "a1", body: "alpha job", state: "pending", position: 0, createdAt: sameSecond }),
      aMemo({ id: "a2", body: "beta job", state: "pending", position: 1, createdAt: sameSecond }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 2, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await click(opts()[0]);
    await click(opts()[0]);
    expect(titles(chips())).toEqual(["alpha job", "beta job"]);
    servedMemos = servedMemos.map((x) => (x.id === "a1" ? { ...x, position: 5 } : x));
    await bump();
    expect(titles(chips())).toEqual(["beta job", "alpha job"]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a2", "a1"] });
  });

  it("warns on a chosen memo that left the offer while the editor was open", async () => {
    await chooseThenLose();
    expect(chips()[0].textContent).toContain(t("memo.after_gone"));
  });

  it("says the same, and nothing about the category, when it left the offer in the same category", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await click(opts()[2]); // a1
    // Another device makes a1 wait for the edited memo: still in the category, but it would close a loop.
    servedMemos = servedMemos.map((x) => (x.id === "a1" ? { ...x, after: ["me"] } : x));
    await bump();
    expect(chips()).toHaveLength(1);
    expect(chips()[0].textContent).toContain(t("memo.after_gone"));
    expect(t("memo.after_gone")).not.toMatch(/categor|カテゴリ/);
  });

  it("brings the active option into view when a new search puts another memo at the same index", async () => {
    seed();
    const seen: string[] = [];
    const orig = Element.prototype.scrollIntoView;
    Element.prototype.scrollIntoView = function (this: Element) {
      seen.push(this.textContent ?? "");
    };
    try {
      await renderSection();
      await openEditor("the edited one");
      await focusSearch();
      await typeSearch("keys");
      seen.length = 0;
      await typeSearch("importer");
      // Index 0 both times, a different memo: the list may have been scrolled away from it.
      expect(seen.join()).toContain("Importer docs");
    } finally {
      Element.prototype.scrollIntoView = orig;
    }
  });

  const cacheSeven = () =>
    useWorkItemStore.setState({
      payload: { items: [workItem("acme/app#7", "Crash when saving")], queries: [], sessions: [], fetchedAt: "", running: false } as never,
      loaded: true,
    });
  const exOf = (o: HTMLElement) => o.querySelector(".memo-after-excerpt")?.textContent ?? "";

  it("tells apart memos whose text starts the same, from where they first differ, even beside a short one that differs early", async () => {
    servedCats = [queueCat];
    const common = "Handle the import of the nightly accounting export for the webshop and report back with ";
    servedMemos = [
      aMemo({ id: "a1", body: common + "a retry plan", state: "pending", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: common + "a skip of the bad rows", state: "pending", position: 1, reference: ref("acme/app#7") }),
      // Differs from the first character: it must not hide where the long two differ.
      aMemo({ id: "a3", body: "Rotate the keys", state: "pending", position: 2, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 3, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    servedMemos.forEach((x) => (x.createdAt = x.id === "me" ? x.createdAt : "2026-09-09T00:00:01Z"));
    cacheSeven();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    const ex = opts().map(exOf);
    expect(new Set(ex).size).toBe(3);
    const text = ex.join("\n");
    expect(text).toContain("a retry plan");
    expect(text).toContain("a skip of the bad rows");
    expect(new Set(opts().map((o) => o.getAttribute("aria-label"))).size).toBe(3);
  });

  it("numbers identical memos by their place in the category, chosen or not", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "a3", body: "same again", state: "pending", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a4", body: "same again", state: "pending", position: 1, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 2, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    servedMemos.forEach((x) => (x.createdAt = x.id === "me" ? x.createdAt : "2026-09-09T00:00:01Z"));
    cacheSeven();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    expect(opts().map(exOf)).toEqual(["#2 same again", "#1 same again"]);
    await click(opts()[0]); // the one numbered #2
    // It keeps its number in the chip, and the one left keeps #1.
    expect(chips().map(exOf)).toEqual(["#2 same again"]);
    expect(opts().map(exOf)).toEqual(["#1 same again"]);
    expect(chips()[0].querySelector("button")!.getAttribute("aria-label")).toContain("#2");
    await click(opts()[0]);
    expect(chips().map(exOf)).toEqual(["#1 same again", "#2 same again"]);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a3", "a4"] });
  });

  it("tells apart notes that come out the same from separate branches of the comparison", async () => {
    servedCats = [queueCat];
    const lead = "Handle the nightly accounting export of the webshop and its refunds, then report back, using the template: ";
    servedMemos = [
      aMemo({ id: "a1", body: "Alpha: " + lead + "retry imports", state: "pending", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: "Alpha: " + lead + "skip bad rows", state: "pending", position: 1, reference: ref("acme/app#7") }),
      aMemo({ id: "a3", body: "Beta: " + lead + "retry imports", state: "pending", position: 2, reference: ref("acme/app#7") }),
      aMemo({ id: "a4", body: "Beta: " + lead + "skip bad rows", state: "pending", position: 3, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 4, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    servedMemos.forEach((x) => (x.createdAt = x.id === "me" ? x.createdAt : "2026-09-09T00:00:01Z"));
    cacheSeven();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await typeSearch("retry imports");
    expect(opts()).toHaveLength(2);
    // Two different memos, and nothing in what is shown or read out says which is which but the number.
    expect(new Set(opts().map((o) => o.getAttribute("aria-label"))).size).toBe(2);
    expect(opts().map(exOf).every((x) => /^#[13] /.test(x))).toBe(true);
    // The number belongs to the memo: the same one is #1 (the earlier in the category) chosen or not.
    const first = opts().find((o) => exOf(o).startsWith("#1"))!;
    await click(first);
    expect(chips().map(exOf)[0]).toMatch(/^#1 /);
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a1"] });
  });

  it("keeps the keyboard on the same memo when a poll puts a new candidate above it", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await pressKey("ArrowDown"); // a2, the second
    expect(opts()[1].getAttribute("aria-selected")).toBe("true");
    servedMemos = [...servedMemos, aMemo({ id: "a4", body: "newer job", state: "pending", position: 2.5, createdAt: "2026-09-09T00:00:02Z" })];
    await bump();
    expect(titles(opts())).toEqual(["newer job", "rotate the keys", "Importer docs", "Crash when saving"]);
    const active = opts().filter((o) => o.getAttribute("aria-selected") === "true");
    expect(titles(active)).toEqual(["Importer docs"]);
    expect(search().getAttribute("aria-activedescendant")).toBe(active[0].id);
    await pressKey("Enter");
    expect(titles(chips())).toEqual(["Importer docs"]);
  });

  it("adds nothing on Enter when the memo the keyboard pointed at left the list, until another is pointed at", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await pressKey("ArrowDown"); // a2
    servedMemos = servedMemos.map((x) => (x.id === "a2" ? { ...x, category: "elsewhere" } : x));
    await bump();
    expect(opts().some((o) => o.getAttribute("aria-selected") === "true")).toBe(false);
    expect(search().getAttribute("aria-activedescendant")).toBeNull();
    await pressKey("Enter");
    expect(chips()).toHaveLength(0);
    await pressKey("ArrowDown");
    expect(opts()[0].getAttribute("aria-selected")).toBe("true");
    await pressKey("Enter");
    expect(titles(chips())).toEqual(["rotate the keys"]);
  });

  it("never lets a number be mistaken for text: a memo whose text reads like a generated number", async () => {
    servedCats = [queueCat];
    servedMemos = [
      aMemo({ id: "a1", body: "retry imports", state: "pending", position: 0, reference: ref("acme/app#7") }),
      aMemo({ id: "a2", body: "retry imports", state: "pending", position: 1, reference: ref("acme/app#7") }),
      aMemo({ id: "a3", body: "#1 retry imports", state: "pending", position: 2, reference: ref("acme/app#7") }),
      aMemo({ id: "me", body: "the edited one", state: "held", position: 3, createdAt: "2026-09-09T00:00:03Z" }),
    ];
    servedMemos.forEach((x) => (x.createdAt = x.id === "me" ? x.createdAt : "2026-09-09T00:00:01Z"));
    cacheSeven();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await typeSearch("retry imports");
    const nums = opts().map((o) => o.querySelector(".memo-after-num")!.textContent);
    // The newest first: a3, a2, a1; every one has a number of its own, in a part of its own.
    expect(nums).toEqual(["#3", "#2", "#1"]);
    expect(new Set(opts().map((o) => o.getAttribute("aria-label"))).size).toBe(3);
    // The one numbered #1 is the earliest, whatever the text of another one says.
    await click(opts()[2]);
    expect(chips()[0].querySelector(".memo-after-num")!.textContent).toBe("#1");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("me", { body: "the edited one", category: "inbox", after: ["a1"] });
  });

  it("fixes the automatically pointed first memo too, so a poll above it does not change what Enter adds", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    expect(opts()[0].getAttribute("aria-selected")).toBe("true"); // a3, pointed without a key press
    servedMemos = [...servedMemos, aMemo({ id: "a4", body: "newer job", state: "pending", position: 2.5, createdAt: "2026-09-09T00:00:02Z" })];
    await bump();
    expect(titles(opts())[0]).toBe("newer job");
    expect(titles(opts().filter((o) => o.getAttribute("aria-selected") === "true"))).toEqual(["rotate the keys"]);
    await pressKey("Enter");
    expect(titles(chips())).toEqual(["rotate the keys"]);
  });

  it("the same with a typed search: what was first stays the pointed one", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await typeSearch("the");
    expect(titles(opts())[0]).toBe("rotate the keys");
    servedMemos = [...servedMemos, aMemo({ id: "a4", body: "the newer job", state: "pending", position: 2.5, createdAt: "2026-09-09T00:00:02Z" })];
    await bump();
    expect(titles(opts())[0]).toBe("the newer job");
    await pressKey("Enter");
    expect(titles(chips())).toEqual(["rotate the keys"]);
  });

  it("does not point at a memo again that left the list and came back, until the member does", async () => {
    seed();
    await renderSection();
    await openEditor("the edited one");
    await focusSearch();
    await pressKey("ArrowDown"); // a2
    const away = servedMemos;
    servedMemos = servedMemos.map((x) => (x.id === "a2" ? { ...x, category: "elsewhere" } : x));
    await bump();
    expect(opts().some((o) => o.getAttribute("aria-selected") === "true")).toBe(false);
    servedMemos = away;
    await bump();
    expect(titles(opts())).toContain("Importer docs");
    expect(opts().some((o) => o.getAttribute("aria-selected") === "true")).toBe(false);
    await pressKey("Enter");
    expect(chips()).toHaveLength(0);
  });

  it("moves the focus on when a chip is removed from the keyboard", async () => {
    seed();
    servedMemos = servedMemos.map((x) => (x.id === "me" ? { ...x, after: ["a1", "a2"] } : x));
    await renderSection();
    await openEditor("the edited one");
    const x = (i: number) => chips()[i].querySelector<HTMLButtonElement>(".memo-after-x")!;
    await act(async () => void x(0).focus());
    await click(x(0));
    // The next chip's ×, then, when none is left, the search field.
    expect(chips()).toHaveLength(1);
    expect(document.activeElement).toBe(x(0));
    await click(x(0));
    expect(chips()).toHaveLength(0);
    expect(document.activeElement).toBe(search());
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
