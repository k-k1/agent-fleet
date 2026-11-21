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
const memoCategoryCreate = vi.fn(async (_input: { repo: string; name: string }) => ({ id: "cnew" }) as unknown as MemoCategory);
const memoUpdate = vi.fn(async (_id: string, _patch: unknown) => ({}));

vi.mock("./api.ts", () => ({
  memoList: async () => servedMemos,
  memoCategoryList: async () => servedCats,
  memoCreate: (input: MemoInput) => memoCreate(input),
  memoUpdate: (id: string, patch: unknown) => memoUpdate(id, patch),
  memoDelete: async () => ({ ok: true }),
  memoCategoryCreate: (input: { repo: string; name: string }) => memoCategoryCreate(input),
  memoCategoryUpdate: async () => ({}),
  memoCategoryDelete: async () => ({ ok: true }),
  memoPasteImage: async () => ({ status: 500 }),
  memoImageGC: async () => ({}),
  memoQueueList: async () => ({ tenantEnabled: false, queues: [] }),
  memoRelease: async () => ({}),
  memoQueuePut: async () => ({}),
  memoQueueDelete: async () => ({ ok: true }),
  memoQueueFire: async () => ({}),
  memoQueueDeliveries: async () => [],
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
  memoUpdate.mockClear();
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

// Expanding a memo is a click on its body, but the SECOND click opens the editor — so
// without an explicit collapse control a long note stays expanded for good and owns the
// left pane. The control only appears on a memo long enough to be clamped.
describe("collapsing an expanded memo", () => {
  const row = () => must(document.querySelector<HTMLElement>(".memo-row"), "memo row");
  const body = () => must(row().querySelector<HTMLElement>(".memo-body"), "memo body");
  const less = () => row().querySelector<HTMLButtonElement>(".memo-less");

  it("expands, collapses again, and never enters the editor on the way", async () => {
    servedMemos = [aMemo({ id: "m1", body: "x".repeat(200) })];
    await render();
    expect(row().classList.contains("exp")).toBe(false);
    expect(less()).toBe(null);

    await click(body());
    expect(row().classList.contains("exp")).toBe(true);

    await click(must(less(), "collapse control"));
    expect(row().classList.contains("exp")).toBe(false);
    // The click must not bubble into the body handler, which would have opened the editor.
    expect(document.querySelector(".memo-row.editing")).toBe(null);
    expect(document.querySelector(".memo-edit-text")).toBe(null);
  });

  it("leaves a short memo without a collapse control", async () => {
    servedMemos = [aMemo({ id: "m1", body: "short" })];
    await render();
    await click(body());
    expect(less()).toBe(null);
  });
});

describe("the mark of a note the setting approved", () => {
  const marks = () => [...document.querySelectorAll<HTMLElement>(".memo-row .memo-qorigin")].map((e) => e.textContent);
  it("is drawn on a pending note that the switch released, and on no other", async () => {
    servedMemos = [
      aMemo({ id: "m1", state: "pending", provenance: "workspace", producer: "s1", releasedBy: "setting" }),
      aMemo({ id: "m2", state: "pending", provenance: "workspace", producer: "s2" }),
      aMemo({ id: "m3", state: "held", provenance: "workspace", producer: "s3", releasedBy: "setting" }),
    ];
    await render();
    const accepted = marks().filter((t) => t === "承認なしで受け付け" || t === "Accepted without approval");
    expect(accepted).toHaveLength(1);
  });
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

  // The Control Plane makes the category row when a note arrives under a name it lacks, so the
  // Console files the note and stops there.
  it("leaves the row of a new category to the server", async () => {
    servedCats = [];
    servedMemos = [aMemo({ id: "m1", category: "Legacy" })];
    await render();

    await click(addBtn("Legacy"));
    await type("hello");
    await click(submit());
    expect(created).toEqual([{ kind: "text", body: "hello", repo: "", category: "Legacy" }]);
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

  // A group a member already has under a worktree bucket keeps its repo: what is chosen by
  // hand is not normalised, only what the Console derives.
  it("keeps an existing worktree bucket's repo when adding into it", async () => {
    servedCats = [aCat({ id: "c4", repo: "app@one", name: "Bugs" })];
    await render();

    await click(addBtn("Bugs"));
    await type("crash on save");
    await click(submit());
    expect(created).toEqual([{ kind: "text", body: "crash on save", repo: "app@one", category: "Bugs" }]);
  });

  it("keeps an existing worktree bucket's repo when a memo is dragged onto it", async () => {
    servedCats = [aCat({ id: "c4", repo: "app@one", name: "Bugs" })];
    servedMemos = [aMemo({ id: "m9", repo: "", category: "" })];
    await render();

    const grip = must(document.querySelector<HTMLElement>(".memo-grip"), "memo grip");
    const dt = { setData: () => {}, types: [], files: [], effectAllowed: "" };
    const fire = async (el: Element, type: string) =>
      act(async () => {
        const ev = new Event(type, { bubbles: true, cancelable: true });
        Object.defineProperty(ev, "dataTransfer", { value: dt });
        el.dispatchEvent(ev);
      });
    await fire(grip, "dragstart");
    await fire(groupFor("Bugs"), "drop");
    await settle();
    expect(memoUpdate).toHaveBeenCalledWith("m9", { position: 0, category: "Bugs", repo: "app@one" });
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

// The compose shortcut can arrive while the composer is already open with a draft: the left
// pane keeps the memo view mounted while another view is on screen. Nothing about the composer
// changes then, and the shortcut still has to take focus, or what is typed next goes wherever
// focus was left (a terminal).
describe("compose request", () => {
  const raf = () => act(async () => void (await new Promise((r) => requestAnimationFrame(() => r(null)))));

  it("takes focus again when the composer is already open", async () => {
    await render();
    await act(async () => useMemoStore.getState().requestCompose());
    await raf();
    const text = must(composer()?.querySelector<HTMLTextAreaElement>(".memo-add-text"), "composer textarea");
    expect(document.activeElement).toBe(text);

    const elsewhere = document.createElement("input");
    document.body.appendChild(elsewhere);
    elsewhere.focus();
    expect(document.activeElement).toBe(elsewhere);

    await act(async () => useMemoStore.getState().requestCompose());
    await raf();
    expect(document.activeElement).toBe(text);
    elsewhere.remove();
  });
});

// "Start with this" (#3289) is how a draft set aside from the Start working dialog comes back,
// so it lives on the row menu of the member's own text memos and seeds the launch.
describe("starting a session from a memo", () => {
  const openMenu = async () => click(must(document.querySelector(".memo-menu-btn"), "row menu button"));
  const item = (label: string) =>
    [...document.querySelectorAll<HTMLButtonElement>(".ui-menu-item")].find((b) => b.textContent?.includes(label));

  it("seeds the launch with the memo's text and files", async () => {
    const { useLaunchSeed } = await import("../repos/store.ts");
    useLaunchSeed.getState().clear();
    const file = { path: "/x/paste-3-run.log", name: "paste-3-run.log" };
    servedMemos = [aMemo({ id: "m1", body: "下書き", attachments: [file] })];
    await render();
    // A non-image file shows as a named tile, not a broken thumbnail.
    expect(document.querySelector(".memo-thumb.file")?.textContent).toContain("run.log");

    await openMenu();
    await click(must(item("Start with this"), "Start with this"));
    expect(useLaunchSeed.getState().prompt).toBe("下書き");
    expect(useLaunchSeed.getState().attachments).toEqual([file]);
    useLaunchSeed.getState().clear();
  });

  it("is not offered on a file memo", async () => {
    servedMemos = [aMemo({ id: "m1", kind: "file", refPath: "~/repos/app/a.ts" })];
    await render();
    await openMenu();
    expect(item("Send this memo")).toBeTruthy();
    expect(item("Start with this")).toBeUndefined();
  });
});

describe("repository group + button", () => {
  const repoAdd = (repo: string) =>
    must(
      [...document.querySelectorAll<HTMLElement>(".memo-repo")]
        .find((el) => el.querySelector(".memo-repo-name")?.textContent === repo)
        ?.querySelector<HTMLButtonElement>(".memo-repo-add"),
      `"+" of repo "${repo}"`,
    );

  // A category made without the group's repo lands in the unfiled area and can never run
  // repository work, so the repo must reach the create call.
  it("creates the category with that group's repo and opens it for rename", async () => {
    servedCats = [aCat({ id: "c3", repo: "app", name: "Bugs" }), aCat({ id: "c4", repo: "app2", name: "Other" })];
    await render();
    await click(repoAdd("app"));
    expect(memoCategoryCreate).toHaveBeenCalledTimes(1);
    expect(memoCategoryCreate.mock.calls[0][0].repo).toBe("app");

    servedCats = [...servedCats, aCat({ id: "cnew", repo: "app", name: "New category", position: 1 })];
    await act(async () => useMemoStore.getState().bump());
    await settle();
    const block = must(
      [...document.querySelectorAll<HTMLElement>(".memo-repo")].find(
        (el) => el.querySelector(".memo-repo-name")?.textContent === "app",
      ),
      "app block",
    );
    expect(block.querySelector(".memo-cat-rename")).not.toBe(null);
  });

  it("sends each group's own repo", async () => {
    servedCats = [aCat({ id: "c3", repo: "app", name: "Bugs" }), aCat({ id: "c4", repo: "app2", name: "Other" })];
    await render();
    await click(repoAdd("app2"));
    expect(memoCategoryCreate.mock.calls[0][0].repo).toBe("app2");
  });

  it("lets the bottom button start a group for a workspace repository", async () => {
    const { useReposStore } = await import("../repos/store.ts");
    useReposStore.setState({
      repos: [
        { name: "newrepo" },
        { name: "newrepo@wt", worktree: true, parent: "newrepo" },
      ] as never,
    });
    servedCats = [aCat({ id: "c3", repo: "app", name: "Bugs" })];
    await render();
    const sel = must(document.querySelector<HTMLSelectElement>(".memo-addcat-repo"), "repo select");
    expect([...sel.options].map((o) => o.value)).toEqual(["", "newrepo"]);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(sel, "newrepo");
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(must(document.querySelector<HTMLButtonElement>(".memo-addcat"), "bottom add button"));
    expect(memoCategoryCreate.mock.calls[0][0].repo).toBe("newrepo");

    servedCats = [...servedCats, aCat({ id: "cnew", repo: "newrepo", name: "New category" })];
    await act(async () => useMemoStore.getState().bump());
    await settle();
    expect([...document.querySelectorAll(".memo-repo-name")].map((e) => e.textContent)).toContain("newrepo");
    useReposStore.setState({ repos: [] });
  });

  it("keeps the bottom button for the unfiled area", async () => {
    servedCats = [aCat({ id: "c3", repo: "app", name: "Bugs" })];
    await render();
    await click(must(document.querySelector<HTMLButtonElement>(".memo-addcat"), "bottom add button"));
    expect(memoCategoryCreate).toHaveBeenCalledTimes(1);
    expect(memoCategoryCreate.mock.calls[0][0].repo).toBe("");
  });
});
