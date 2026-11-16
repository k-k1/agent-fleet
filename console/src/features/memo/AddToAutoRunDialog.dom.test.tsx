// "Add to auto-run" on a work item: the picker's defaults (the launch repository, as its parent
// for a worktree), the order of the categories, what is sent (a pointer and the member's own
// note, never the item's text) and what is said afterwards, including for an item that is
// already in the category.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { MemoCategory, MemoInput } from "../../types/memo.ts";
import type { Repo } from "../repos/store.ts";

let servedCats: MemoCategory[] = [];
let servedQueues: unknown = { tenantEnabled: true, queues: [] };
const memoCreate = vi.fn(async (_i: MemoInput): Promise<Record<string, unknown>> => ({ id: "new" }));
vi.mock("./api.ts", () => ({
  memoCategoryList: async () => servedCats,
  memoQueueList: async () => servedQueues,
  memoCreate: (i: MemoInput) => memoCreate(i),
}));

const { AddToAutoRunDialog } = await import("./AddToAutoRunDialog.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { t } = await import("../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;
const onClose = vi.fn();

const reference = { provider: "github", key: "acme/app#12", url: "https://github.com/acme/app/issues/12" };
const repo = (name: string, over: Partial<Repo> = {}): Repo => ({ name, ...over }) as Repo;
const cat = (id: string, name: string, over: Partial<MemoCategory> = {}): MemoCategory => ({
  id,
  repo: "app",
  name,
  position: 0,
  createdAt: "",
  ...over,
});
const queue = (categoryId: string, enabled: boolean) => ({ categoryId, repo: "app", category: "x", enabled });

const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function open(repos: Repo[], defaultRepo: string) {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <AddToAutoRunDialog reference={reference} repos={repos} defaultRepo={defaultRepo} onClose={onClose} />
      </ToastProvider>,
    );
  });
  await settle();
}
const sel = (label: string) =>
  [...document.querySelectorAll<HTMLLabelElement>("label.wi-sfield")]
    .find((l) => l.querySelector("span")?.textContent === label)!
    .querySelector("select") as HTMLSelectElement;
async function choose(el: HTMLSelectElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("change", { bubbles: true }));
  });
}
async function typeNote(v: string) {
  const ta = document.querySelector("textarea") as HTMLTextAreaElement;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(ta, v);
    ta.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const confirmBtn = () => document.querySelector<HTMLButtonElement>(".confirm-actions button[data-autofocus]")!;
async function press() {
  await act(async () => confirmBtn().click());
  await settle();
}

beforeEach(() => {
  servedCats = [];
  servedQueues = { tenantEnabled: true, queues: [] };
  memoCreate.mockClear();
  memoCreate.mockResolvedValue({ id: "new" });
  onClose.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  document.body.querySelectorAll(".ui-confirm-backdrop").forEach((e) => e.remove());
});

describe("the picker", () => {
  it("starts on the repository a launch would default to", async () => {
    await open([repo("app"), repo("web")], "web");
    expect(sel(t("memo.addrun_repo")).value).toBe("web");
  });

  it("files under the parent repository when the default is a worktree", async () => {
    await open([repo("app"), repo("app@feat", { worktree: true, parent: "app" })], "app@feat");
    expect(sel(t("memo.addrun_repo")).value).toBe("app");
    // A worktree folder is never offered as a place to file.
    expect([...sel(t("memo.addrun_repo")).options].map((o) => o.value)).toEqual(["app"]);
  });

  it("falls back to the folder's base name when the worktree's parent record is gone", async () => {
    await open([repo("app"), repo("app@feat", { worktree: true })], "app@feat");
    expect(sel(t("memo.addrun_repo")).value).toBe("app");
  });

  it("starts unchosen when nothing resolved, and cannot be pressed", async () => {
    await open([repo("app")], "");
    expect(sel(t("memo.addrun_repo")).value).toBe("");
    expect(confirmBtn().disabled).toBe(true);
  });

  it("lists the auto-run categories first and marked, the others below, and says an ordinary one is an ordinary note", async () => {
    servedCats = [cat("c1", "plain", { position: 0 }), cat("c2", "runs", { position: 1, queue: true }), cat("c3", "paused-off", { position: 2, queue: true }), cat("c4", "elsewhere", { repo: "web" })];
    servedQueues = { tenantEnabled: true, queues: [queue("c2", true), queue("c3", false)] };
    await open([repo("app"), repo("web")], "app");
    const groups = [...sel(t("memo.addrun_category")).querySelectorAll("optgroup")];
    expect(groups.map((g) => g.label)).toEqual([t("memo.addrun_group_auto"), t("memo.addrun_group_plain")]);
    expect([...groups[0].querySelectorAll("option")].map((o) => o.textContent)).toEqual([
      `runs — ${t("memo.autorun_badge")}`,
      `paused-off — ${t("memo.autorun_badge_off")}`,
    ]);
    expect([...groups[1].querySelectorAll("option")].map((o) => o.textContent)).toEqual(["plain"]);

    await choose(sel(t("memo.addrun_category")), "c1");
    expect(document.body.textContent).toContain(t("memo.addrun_plain_hint"));
    await choose(sel(t("memo.addrun_category")), "c2");
    expect(document.body.textContent).not.toContain(t("memo.addrun_plain_hint"));
  });
});

describe("adding", () => {
  beforeEach(() => {
    servedCats = [cat("c2", "runs", { queue: true })];
    servedQueues = { tenantEnabled: true, queues: [queue("c2", true)] };
  });

  it("sends the pointer and the member's note, nothing of the item, and says where it went", async () => {
    await open([repo("app")], "app");
    await choose(sel(t("memo.addrun_category")), "c2");
    await typeNote("  start with the parser  ");
    await press();
    expect(memoCreate).toHaveBeenCalledWith({ kind: "text", repo: "app", category: "runs", body: "start with the parser", reference });
    expect(document.body.textContent).toContain(t("memo.addrun_done", { key: "acme/app#12", category: "runs", repo: "app" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("sends an empty body when no note was typed", async () => {
    await open([repo("app")], "app");
    await choose(sel(t("memo.addrun_category")), "c2");
    await press();
    expect(memoCreate.mock.calls[0][0].body).toBe("");
  });

  it("says that nothing was added for an item that is already there, and that the typed note was not stored", async () => {
    memoCreate.mockResolvedValue({ id: "old", existing: true });
    await open([repo("app")], "app");
    await choose(sel(t("memo.addrun_category")), "c2");
    await typeNote("my note");
    await press();
    const where = { key: "acme/app#12", category: "runs", repo: "app" };
    expect(document.body.textContent).toContain(t("memo.addrun_existing", where));
    expect(document.body.textContent).toContain(t("memo.addrun_existing_note"));
    expect(document.body.textContent).not.toContain(t("memo.addrun_done", where));
  });

  it("does not claim a note was lost when none was typed", async () => {
    memoCreate.mockResolvedValue({ id: "old", existing: true });
    await open([repo("app")], "app");
    await choose(sel(t("memo.addrun_category")), "c2");
    await press();
    expect(document.body.textContent).toContain(t("memo.addrun_existing", { key: "acme/app#12", category: "runs", repo: "app" }));
    expect(document.body.textContent).not.toContain(t("memo.addrun_existing_note"));
  });

  it("keeps the dialog open and says so when the Control Plane refuses", async () => {
    memoCreate.mockResolvedValue({ error: { code: "memo_ref_invalid", message: "" } });
    await open([repo("app")], "app");
    await choose(sel(t("memo.addrun_category")), "c2");
    await press();
    expect(onClose).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain(t("err.memo_ref_invalid"));
  });
});
