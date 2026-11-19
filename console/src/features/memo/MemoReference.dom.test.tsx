// A note that points at a work item (ADR 0137 §4): its row in the list (link, cached title, a
// reference-only note, keeping or removing the reference on an edit), the approval card that must
// never hide it, the notice while the workspace cannot take such notes, and a refusal for an item
// that is already in the category.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory, MemoQueue } from "../../types/memo.ts";
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
const { MemoQueueNotices } = await import("./MemoQueueNotices.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkItemStore } = await import("../workitems/store.ts");
const { useWorkItemModal } = await import("../workitems/modal.ts");
const { t } = await import("../../lib/i18n/index.ts");
const { readMemoQueues } = await import("./queues.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const ref = { provider: "github", key: "acme/app#12", url: "https://github.com/acme/app/issues/12" };
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
const cached = (over: Partial<WorkItem>): WorkItem =>
  ({
    id: "w1",
    queryId: "q",
    provider: "github",
    kind: "issue",
    key: "Acme/App#12",
    title: "Crash when saving",
    state: "open",
    url: ref.url,
    assignee: "",
    labels: [],
    labelColors: {},
    repo: "Acme/App",
    updatedAt: "",
    checks: { state: "", total: 0, failed: 0, pending: 0 },
    mergeable: "",
    ...over,
  }) as WorkItem;
const cacheItems = (...items: WorkItem[]) =>
  useWorkItemStore.setState({ payload: { items, queries: [], sessions: [], fetchedAt: "", running: false } as never, loaded: true });

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
const row = () => document.querySelector<HTMLElement>(".memo-row")!;
const keyButton = () => row().querySelector<HTMLButtonElement>("button.memo-reflink-key");
const link = () => row().querySelector<HTMLAnchorElement>("a.memo-reflink-key");

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

describe("a row with a reference", () => {
  it("shows the key as a link to the item with the member's note below it", async () => {
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    expect(link()?.textContent).toBe("acme/app#12");
    expect(link()?.getAttribute("href")).toBe(ref.url);
    expect(link()?.getAttribute("target")).toBe("_blank");
    expect(row().querySelector(".memo-reflink-mark .codicon-issues")).not.toBeNull();
    const html = row().innerHTML;
    expect(html.indexOf("memo-reflink")).toBeLessThan(html.indexOf("my note"));
    // Not in the cache: the key alone, no title.
    expect(row().querySelector(".memo-reflink-title")).toBeNull();
  });

  it("shows the cached item's title, matching the key without regard to case", async () => {
    cacheItems(cached({ key: "Acme/App#12" }));
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    expect(row().querySelector(".memo-reflink-title")?.textContent).toBe("Crash when saving");
    // The key reads as the work-item list writes it, though the note holds it in lower case.
    expect(keyButton()?.textContent).toBe("Acme/App#12");
    // Cached: the key is an in-app button, not an external link.
    expect(link()).toBeNull();
  });

  it("opens the in-app work item modal from a cached item's key, without toggling the row", async () => {
    cacheItems(cached({ key: "Acme/App#12" }));
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    const openDetail = vi.spyOn(useWorkItemModal.getState(), "openDetail").mockImplementation(() => {});
    await click(keyButton()!);
    expect(openDetail).toHaveBeenCalledTimes(1);
    expect((openDetail.mock.calls[0][0] as WorkItem).key).toBe("Acme/App#12");
    expect(row().classList.contains("exp")).toBe(false);
    openDetail.mockRestore();
  });

  it("keeps the external link when the item is not cached", async () => {
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    expect(keyButton()).toBeNull();
    expect(link()?.getAttribute("href")).toBe(ref.url);
  });

  it("keeps state and producer below the body for a note without a reference, and for a file note", async () => {
    servedMemos = [
      aMemo({ id: "a", body: "plain note", state: "pending" } as Partial<Memo>),
      aMemo({ id: "b", kind: "file", refPath: "docs/x.md", body: "file note", state: "pending" } as Partial<Memo>),
    ];
    await renderSection();
    expect(document.querySelectorAll(".memo-row")).toHaveLength(2);
    for (const r of document.querySelectorAll<HTMLElement>(".memo-row")) {
      const html = r.innerHTML;
      expect(html.indexOf("memo-qmeta")).toBeGreaterThan(html.indexOf("memo-text"));
      expect(r.querySelector(".memo-reflink")).toBeNull();
    }
  });

  it("leaves the external link in the edit view even when the item is cached", async () => {
    cacheItems(cached({ key: "Acme/App#12" }));
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    expect(row().querySelector("button.memo-reflink-key")).toBeNull();
    expect(row().querySelector("a.memo-reflink-key")?.getAttribute("href")).toBe(ref.url);
  });

  it("draws the state tag on the reference's line and no separate expand-hint line", async () => {
    servedMemos = [aMemo({ reference: ref, body: "x".repeat(100), state: "pending" } as Partial<Memo>)];
    await renderSection();
    expect(row().querySelector(".memo-reflink .memo-qmeta")).not.toBeNull();
    expect(row().querySelector(".memo-more")).toBeNull();
  });

  it("does not fold the case of a key it should not: jira keys, and another provider's item", async () => {
    cacheItems(cached({ provider: "jira", key: "PROJ-12", title: "jira one" }), cached({ provider: "bitbucket", key: "acme/app#12", title: "bitbucket one" }));
    servedMemos = [aMemo({ reference: { provider: "github", key: "acme/app#12", url: ref.url } })];
    await renderSection();
    expect(row().querySelector(".memo-reflink-title")).toBeNull();
  });

  it("draws a PR's mark for a cached pull request", async () => {
    cacheItems(cached({ kind: "pr", title: "Fix" }));
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    expect(row().querySelector(".memo-reflink-mark .codicon-git-pull-request")).not.toBeNull();
  });

  it("is a valid row when the note is only a reference", async () => {
    servedMemos = [aMemo({ body: "", reference: ref })];
    await renderSection();
    expect(document.querySelectorAll(".memo-row")).toHaveLength(1);
    expect(link()?.textContent).toBe("acme/app#12");
    expect(row().querySelector(".memo-text")).toBeNull();
  });

  it("shows a reference whose address is not http(s) as text, never as a link", async () => {
    servedMemos = [aMemo({ reference: { ...ref, url: "javascript:alert(1)" } })];
    await renderSection();
    expect(link()).toBeNull();
    expect(row().querySelector(".memo-reflink-key")?.textContent).toBe("acme/app#12");
  });

  it("keeps the reference when only the text is edited: the patch has no reference key", async () => {
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    const body = row().querySelector<HTMLElement>(".memo-body")!;
    await click(body); // expand
    await click(row().querySelector<HTMLElement>(".memo-body")!); // edit
    await click([...row().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("memo.save"))!);
    expect(memoUpdate).toHaveBeenCalledTimes(1);
    const patch = memoUpdate.mock.calls[0][1] as Record<string, unknown>;
    expect(patch).toEqual({ body: "my note", category: "inbox" });
    expect("reference" in patch).toBe(false);
  });

  it("removes the reference with {} when the member asks and saves, and can take it back", async () => {
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    const btn = (label: string) => [...row().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === label)!;
    await click(btn(t("memo.ref_remove")));
    expect(row().textContent).toContain(t("memo.ref_remove_pending", { key: "acme/app#12" }));
    await click(btn(t("memo.ref_keep")));
    expect(btn(t("memo.ref_remove"))).toBeDefined();
    await click(btn(t("memo.ref_remove")));
    await click(btn(t("memo.save")));
    expect(memoUpdate).toHaveBeenCalledWith("m1", { body: "my note", category: "inbox", reference: {} });
  });

  it("shows the catalogue text for an item already in the category when an edit is refused, and the note stays", async () => {
    memoUpdate.mockResolvedValue({ error: { code: "memo_ref_duplicate", message: "english" } });
    servedMemos = [aMemo({ reference: ref })];
    await renderSection();
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    await click(row().querySelector<HTMLElement>(".memo-body")!);
    await click([...row().querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === t("memo.save"))!);
    expect(document.body.textContent).toContain(t("err.memo_ref_duplicate"));
    expect(document.querySelectorAll(".memo-row")).toHaveLength(1);
  });
});

describe("dragging a note that points at an item", () => {
  const drag = async (from: Element, onto: Element) => {
    const dt = { setData: () => {}, types: [], files: [], effectAllowed: "" };
    const fire = async (el: Element, type: string) =>
      act(async () => {
        const ev = new Event(type, { bubbles: true, cancelable: true });
        Object.defineProperty(ev, "dataTransfer", { value: dt });
        el.dispatchEvent(ev);
      });
    await fire(from, "dragstart");
    await fire(onto, "drop");
    await settle();
  };
  const group = (name: string) => [...document.querySelectorAll<HTMLElement>(".memo-cat")].find((g) => g.querySelector(".memo-cat-name")?.textContent === name)!;

  it("says why on a 409 memo_ref_duplicate, and the note goes back where it was", async () => {
    servedCats = [aCat({ id: "c1", name: "inbox", position: 0 }), aCat({ id: "c2", name: "runs", position: 1, queue: true })];
    servedMemos = [aMemo({ id: "m1", category: "inbox", reference: ref })];
    memoUpdate.mockResolvedValue({ error: { code: "memo_ref_duplicate", message: "english" } });
    await renderSection();
    await drag(document.querySelector(".memo-grip")!, group("runs"));
    expect(memoUpdate).toHaveBeenCalledWith("m1", { position: 0, category: "runs", repo: "" });
    expect(document.body.textContent).toContain(t("err.memo_ref_duplicate"));
    expect(group("inbox").querySelector(".memo-row")).not.toBeNull();
    expect(group("runs").querySelector(".memo-row")).toBeNull();
    // A refused move is not announced as a move.
    expect(document.body.textContent).not.toContain(t("memo.moved", { cat: "runs" }));
  });
});

describe("the approval card", () => {
  const card = (items: Memo[]) => ({ key: "k", provenance: "workspace", producer: "worker-1", items });
  const draw = async (items: Memo[]) => {
    await act(async () => {
      root!.render(
        <MemoReleaseCard card={card(items)} busy={false} changedIds={new Set()} edited={new Set()} awaiting={new Set()} onRelease={() => {}} onConfirm={() => {}} />,
      );
    });
  };

  it("shows the reference of a note a session added, beside its text", async () => {
    cacheItems(cached({}));
    await draw([aMemo({ state: "held", contentHash: "h", reference: ref })]);
    const a = document.querySelector<HTMLAnchorElement>(".memo-release a.memo-reflink-key")!;
    expect(a.textContent).toBe("Acme/App#12");
    expect(a.getAttribute("href")).toBe(ref.url);
    expect(document.querySelector(".memo-release .memo-reflink-title")?.textContent).toBe("Crash when saving");
    expect(document.querySelector(".memo-release-text")?.textContent).toBe("my note");
    expect(document.querySelector(".memo-release-hint")?.textContent).toContain(t("memo.ref_card_hint"));
  });

  it("shows a reference-only note as its reference, not as an empty dash", async () => {
    await draw([aMemo({ state: "held", contentHash: "h", body: "", reference: ref })]);
    expect(document.querySelector(".memo-release a.memo-reflink-key")?.textContent).toBe("acme/app#12");
    expect(document.querySelector(".memo-release-text")).toBeNull();
  });

  it("shows a reference even when its address is unusable: the release is bound to it", async () => {
    await draw([aMemo({ state: "held", contentHash: "h", body: "x", reference: { provider: "github", key: "acme/app#12", url: "ftp://x" } })]);
    expect(document.querySelector(".memo-release .memo-reflink-key")?.textContent).toBe("acme/app#12");
    expect(document.querySelector(".memo-release a")).toBeNull();
  });

  it("keeps the dash for an empty note with no reference, and says nothing of items", async () => {
    await draw([aMemo({ state: "held", contentHash: "h", body: "" })]);
    expect(document.querySelector(".memo-release-text")?.textContent).toBe("—");
    expect(document.querySelector(".memo-release-hint")?.textContent).not.toContain(t("memo.ref_card_hint"));
  });
});

describe("notes that point at an item and a workspace that cannot take them yet", () => {
  const queue = (over: Record<string, unknown>): MemoQueue =>
    readMemoQueues({ tenantEnabled: true, queues: [{ categoryId: "c1", enabled: true, ...over }] })!.queues[0];
  const draw = async (q: MemoQueue) => {
    await act(async () => void root!.render(<MemoQueueNotices queue={q} busy={false} onResume={() => {}} />));
  };

  it("says it from noRefsAt, in the members' words, and that notes without one still run", async () => {
    await draw(queue({ noRefsAt: "2026-11-17T01:00:00Z" }));
    const text = document.querySelector(".memo-q-noreport")?.textContent;
    expect(text).toBe(t("memo.no_refs_notice"));
    expect(text).not.toMatch(/queue|キュー|slot|delivery/i);
  });

  it("says it from the waiting reason too, once, and draws no raw identifier", async () => {
    await draw(queue({ waiting: { reason: "no_references" } }));
    expect(document.querySelectorAll(".memo-q-noreport")).toHaveLength(1);
    expect(document.querySelector(".memo-q-why")).toBeNull();
    expect(document.body.textContent).not.toContain("no_references");
  });

  it("says nothing when neither is set", async () => {
    await draw(queue({}));
    expect(document.querySelector(".memo-q-notices")).toBeNull();
  });
});
