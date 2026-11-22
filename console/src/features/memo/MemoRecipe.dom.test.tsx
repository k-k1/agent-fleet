// A run recipe a note of an auto-run category carries (ADR 0137 addendum, #4845): the row and the
// approval card badge it, the edit view sets, keeps or clears it (sent only when changed), and the
// held reason `recipe_invalid` has its own words.
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
  memoQueueList: async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", agentKind: "claude", enabled: true }] }),
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
const recipeInputs = () => editRow().querySelectorAll<HTMLInputElement>(".memo-edit-recipe-row input");
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


beforeEach(() => {
  localStorage.clear();
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
});

describe("a note with its own run recipe", () => {
  it("badges the row only when it overrides the category", async () => {
    servedMemos = [
      aMemo({ id: "a", body: "own job", state: "pending", agentKind: "codex", model: "gpt-x", effort: "high" }),
      aMemo({ id: "b", body: "plain job", state: "pending", position: 1 }),
    ];
    await renderSection();
    expect(rowOf("own job").querySelector("[data-recipe-badge]")?.textContent).toBe("codex · gpt-x · high");
    expect(rowOf("plain job").querySelector("[data-recipe-badge]")).toBeNull();
  });

  it("sets the fields the member changed, and sends nothing for an untouched row", async () => {
    servedMemos = [aMemo({ id: "a", body: "own job", state: "pending", agentKind: "codex", model: "gpt-x" })];
    await renderSection();
    await openEditor("own job");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "own job", category: "inbox" });

    await openEditor("own job");
    const [model, effort] = recipeInputs();
    await typeInto(model, "");
    await typeInto(effort, "high");
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "own job", category: "inbox", model: "", effort: "high" });
  });

  it("keeps the recipe it began with as the baseline when a refresh brings another device's change", async () => {
    const { useMemoStore } = await import("./store.ts");
    servedMemos = [aMemo({ id: "a", body: "own job", state: "pending" })];
    await renderSection();
    await openEditor("own job");
    // Another device sets a model while this edit is open; the list is read again.
    servedMemos = [aMemo({ id: "a", body: "own job", state: "pending", model: "theirs" })];
    await act(async () => useMemoStore.getState().bump());
    await settle();
    await click(btn(editRow(), t("memo.save")));
    // A text-only save must not clear what the other device set.
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "own job", category: "inbox" });
  });

  it("does not drop a recipe being typed when the refresh brings a new text", async () => {
    const { useMemoStore } = await import("./store.ts");
    servedMemos = [aMemo({ id: "a", body: "own job", state: "pending" })];
    await renderSection();
    await openEditor("own job");
    await typeInto(recipeInputs()[0], "gpt-typed");
    servedMemos = [aMemo({ id: "a", body: "own job edited elsewhere", state: "pending" })];
    await act(async () => useMemoStore.getState().bump());
    await settle();
    expect(recipeInputs()[0].value).toBe("gpt-typed");
  });

  it("sends no recipe when the note is moved out of the auto-run categories, and says so", async () => {
    servedMemos = [aMemo({ id: "a", body: "own job", state: "pending" })];
    await renderSection();
    await openEditor("own job");
    await typeInto(recipeInputs()[0], "gpt-typed");
    await typeInto(editRow().querySelector<HTMLInputElement>(".memo-edit-cat")!, "plain");
    expect(editRow().textContent).toContain(t("memo.recipe_dropped"));
    await click(btn(editRow(), t("memo.save")));
    expect(memoUpdate).toHaveBeenLastCalledWith("a", { body: "own job", category: "plain" });
  });

  it("offers no recipe row on a note outside an auto-run category", async () => {
    servedCats = [{ ...queueCat, queue: false }];
    servedMemos = [aMemo({ id: "a", body: "plain note" })];
    await renderSection();
    await openEditor("plain note");
    expect(editRow().querySelector(".memo-edit-recipe")).toBeNull();
  });

  it("words recipe_invalid as the recipe, not as a failed prerequisite", async () => {
    servedMemos = [aMemo({ id: "a", body: "bad job", state: "held", agentKind: "codex", heldReason: { after: "", reason: "recipe_invalid" } })];
    await renderSection();
    const row = rowOf("bad job");
    expect(row.textContent).toContain(t("memo.held_recipe_invalid"));
    expect(row.querySelector(".memo-after-held")?.textContent).not.toContain(t("memo.after_r_failed"));
  });

  it("shows the recipe on the approval card, with the held reason", async () => {
    const m = aMemo({ id: "a", body: "bad job", state: "held", provenance: "workspace", producer: "s1", agentKind: "codex", model: "gpt-x", heldReason: { after: "", reason: "recipe_invalid" } });
    await act(async () => {
      root!.render(
        <ToastProvider>
          <MemoReleaseCard card={{ key: "k", provenance: "workspace", producer: "s1", items: [m] } as never} busy={false} changedIds={new Set()} edited={new Set()} awaiting={new Set()} onRelease={() => {}} onConfirm={() => {}} />
        </ToastProvider>,
      );
    });
    expect(document.querySelector(".memo-release-recipe")?.textContent).toBe("codex · gpt-x");
    expect(document.body.textContent).toContain(t("memo.held_recipe_invalid"));
  });
});
