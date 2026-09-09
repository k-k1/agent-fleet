// Render tests for the category "+" on the memo queue's category headers. The button's
// whole point is WHERE the memo lands: it moves the single composer under that header and
// makes the add write into that (repo, category) instead of the free-form category field.
// A regression here is silent — the memo is created, just in the wrong bucket.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoCategory, MemoInput } from "../../types/memo.ts";

let servedMemos: Memo[] = [];
let servedCats: MemoCategory[] = [];
const created: MemoInput[] = [];
const memoCreate = vi.fn(async (input: MemoInput) => {
  created.push(input);
  return { id: "new", ...input } as unknown as Memo;
});
const memoCategoryCreate = vi.fn(async () => ({ id: "cnew" }) as unknown as MemoCategory);

vi.mock("./api.ts", () => ({
  memoList: async () => servedMemos,
  memoCategoryList: async () => servedCats,
  memoCreate: (input: MemoInput) => memoCreate(input),
  memoUpdate: async () => ({}),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: () => memoCategoryCreate(),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
}));
// The PWA share stash reads CacheStorage, which jsdom does not have.
vi.mock("./share.ts", () => ({ consumeShare: async () => null, registerShareSW: () => {} }));

const { MemoQueueSection } = await import("./MemoQueueSection.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useMemoStore } = await import("./store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}

const aMemo = (over: Partial<Memo>): Memo => ({
  id: "m1",
  repo: "",
  category: "",
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
  name: "cat",
  position: 0,
  createdAt: "2026-09-09T00:00:00Z",
  ...over,
});

// The rendered group whose header name matches — the block the composer must move into.
const groupFor = (name: string) =>
  must(
    [...document.querySelectorAll<HTMLElement>(".memo-cat")].find(
      (el) => el.querySelector(".memo-cat-name")?.textContent === name,
    ),
    `category group "${name}"`,
  );
const addBtn = (name: string) =>
  must(groupFor(name).querySelector<HTMLButtonElement>(".memo-cat-add"), `"+" of category "${name}"`);
const composer = () => document.querySelector<HTMLElement>(".memo-add");

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

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}

async function type(text: string): Promise<void> {
  const el = must(composer()?.querySelector<HTMLTextAreaElement>(".memo-add-text"), "composer textarea");
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    setter.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

// Add ("追加"): the composer's only primary button.
const submit = () => must(composer()?.querySelector<HTMLButtonElement>(".ui-btn-primary"), "composer add button");

beforeEach(() => {
  localStorage.clear();
  created.length = 0;
  memoCreate.mockClear();
  memoCategoryCreate.mockClear();
  servedMemos = [];
  servedCats = [];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  vi.useRealTimers();
});

describe("category + button", () => {
  it("moves the composer under the category and writes into it", async () => {
    servedCats = [aCat({ id: "c1", name: "P1" }), aCat({ id: "c2", name: "P2", position: 1 })];
    servedMemos = [aMemo({ id: "m1", category: "P1" })];
    await render();

    // Every group offers one, including the ones holding no memo yet.
    expect(document.querySelectorAll(".memo-cat-add").length).toBe(2);

    await click(addBtn("P2"));
    expect(groupFor("P2").contains(composer())).toBe(true);
    expect(groupFor("P1").contains(composer())).toBe(false);
    // A targeted composer states its destination instead of offering the free-form field,
    // which would let the user type a different category than the one they clicked.
    expect(composer()!.querySelector(".memo-add-cat")).toBe(null);
    expect(composer()!.querySelector(".memo-add-target")?.textContent).toBe("P2");

    await type("hello");
    await click(submit());
    expect(created).toEqual([{ kind: "text", body: "hello", repo: "", category: "P2" }]);
    // P2 already exists as a first-class row — no duplicate category is created.
    expect(memoCategoryCreate).not.toHaveBeenCalled();
  });

  it("keeps a repo bucket's memo in that repo", async () => {
    servedCats = [aCat({ id: "c3", repo: "app", name: "Bugs" })];
    await render();

    await click(addBtn("Bugs"));
    await type("crash on save");
    await click(submit());
    expect(created).toEqual([{ kind: "text", body: "crash on save", repo: "app", category: "Bugs" }]);
  });

  it("unfolds a collapsed category so the composer is visible", async () => {
    localStorage.setItem("af.memo-collapsed", JSON.stringify({ ["\u0000P1"]: true }));
    servedCats = [aCat({ id: "c1", name: "P1" })];
    servedMemos = [aMemo({ id: "m1", category: "P1" })];
    await render();
    expect(groupFor("P1").querySelector(".memo-cat-items")).toBe(null);

    await click(addBtn("P1"));
    expect(groupFor("P1").querySelector(".memo-cat-items")).not.toBe(null);
    expect(groupFor("P1").contains(composer())).toBe(true);
  });

  it("falls back to the section-top composer when its category disappears", async () => {
    servedCats = [aCat({ id: "c1", name: "P1" })];
    await render();
    await click(addBtn("P1"));
    expect(groupFor("P1").contains(composer())).toBe(true);

    // Another device deletes the category while the composer is open on it. Without the
    // liveness check the composer would stay open with nowhere to render — invisible, and
    // the next Ctrl+Enter would file the note under a category that no longer exists.
    servedCats = [aCat({ id: "c2", name: "P2" })];
    await act(async () => {
      useMemoStore.getState().bump();
    });
    await settle();
    expect(composer()!.closest(".memo-cat")).toBe(null);
    expect(composer()!.querySelector(".memo-add-cat")).not.toBe(null);
  });

  it("still adds through the free-form field from the section header", async () => {
    servedCats = [aCat({ id: "c1", name: "P1" })];
    await render();
    // The header "+" (Section actions) opens the untargeted composer.
    const headerAdd = must(
      [...document.querySelectorAll<HTMLButtonElement>(".ui-section-actions button")][0],
      "section header + button",
    );
    await click(headerAdd);
    expect(composer()!.querySelector(".memo-add-cat")).not.toBe(null);
    expect(groupFor("P1").contains(composer())).toBe(false);

    await type("unfiled note");
    await click(submit());
    expect(created).toEqual([{ kind: "text", body: "unfiled note", repo: "", category: "" }]);
  });
});
