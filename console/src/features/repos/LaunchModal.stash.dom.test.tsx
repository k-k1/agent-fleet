// Render tests for moving a Start working draft into the memo queue and back (#3289).
// "Save to memo" must upload every staged file before creating the memo and only then empty
// the box; a memo-seeded dialog must stage the memo's files as real chips that the launch
// uploads like a fresh paste.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Upload = { status: number; path?: string; name?: string };
const pasteMock = vi.fn<(f: File) => Promise<Upload>>();
const createMock = vi.fn<(input: unknown) => Promise<unknown>>();
// Stored memo files served by GET api/memos/images/<name>; a missing name answers 404.
let stored: Record<string, { body: string; type: string }> = {};
// When set, every stored-file GET waits for it (a slow fetch of the memo's files).
let blockFetch: Promise<void> | null = null;

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  getUser: () => "",
  api: async () => ({ branches: [] }),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: () => "",
  errDetail: () => "",
  isTransientErr: () => false,
  memoPasteImage: (f: File) => pasteMock(f),
  memoCreate: (input: unknown) => createMock(input),
  memoDelete: async () => new Response(null, { status: 204 }),
  memoImageURL: (name: string) => `api/memos/images/${name}`,
  raw: async (url: string) => {
    if (blockFetch) await blockFetch;
    const s = stored[url.split("/").pop() || ""];
    // A plain object, not a Response: undici's Response cannot read jsdom's Blob.
    return s ? { ok: true, blob: async () => new Blob([s.body], { type: s.type }) } : { ok: false };
  },
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
import type { LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import type { MemoAttachment } from "../../types/memo.ts";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

function must<T>(el: T | undefined | null, what: string): T {
  if (!el) throw new Error(`not in the DOM: ${what}`);
  return el;
}

const promptBox = () => must(document.querySelector<HTMLTextAreaElement>("textarea"), "first-prompt textarea");
const chips = () => [...document.querySelectorAll(".mirror-attach .ma-chip")];
const stashBtn = () => must(document.querySelector<HTMLButtonElement>(".launch-stash-btn"), "save-to-memo button");
const note = () => document.querySelector(".launch-stash-note")?.textContent || "";
const byText = (t: string) =>
  must([...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes(t)), `button "${t}"`);

async function settle(): Promise<void> {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function render(extra: { initialPrompt?: string; initialAttachments?: MemoAttachment[] } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <LaunchModal repo="app" branch="main" kinds={["claude"]} onClose={() => {}} onLaunch={onLaunch} {...extra} />,
    );
  });
  await settle();
}

async function reopen(): Promise<void> {
  act(() => root?.unmount());
  root = createRoot(host);
  await render();
}

async function type(text: string): Promise<void> {
  const el = promptBox();
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function paste(file: File): Promise<void> {
  const ev = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(ev, "clipboardData", { value: { items: [{ kind: "file", type: file.type, getAsFile: () => file }] } });
  await act(async () => {
    promptBox().dispatchEvent(ev);
  });
  await settle();
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  stored = {};
  blockFetch = null;
  let n = 0;
  pasteMock.mockReset().mockImplementation(async (f) => {
    n++;
    const name = f.type.startsWith("image/") ? `paste-${n}.png` : `paste-${n}-${f.name}`;
    return { status: 201, path: `/home/dev/.cache/agent-fleet/memo-images/${name}`, name };
  });
  createMock.mockReset().mockResolvedValue({ id: "m1" });
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("Save to memo", () => {
  it("is disabled while there is nothing to save", async () => {
    await render();
    expect(stashBtn().disabled).toBe(true);
    await type("x");
    expect(stashBtn().disabled).toBe(false);
  });

  it("uploads every file, creates the memo in the repo bucket, then empties the draft for good", async () => {
    await render();
    await type("  別の作業の下書き  ");
    await paste(new File(["IMG"], "shot.png", { type: "image/png" }));
    await paste(new File(["LOG"], "run.log", { type: "text/plain" }));
    expect(chips()).toHaveLength(2);

    await click(stashBtn());

    expect(pasteMock.mock.calls.map(([f]) => f.name)).toEqual(["shot.png", "run.log"]);
    expect(createMock).toHaveBeenCalledWith({
      kind: "text",
      repo: "app",
      body: "別の作業の下書き",
      attachments: [
        { path: "/home/dev/.cache/agent-fleet/memo-images/paste-1.png", name: "paste-1.png" },
        { path: "/home/dev/.cache/agent-fleet/memo-images/paste-2-run.log", name: "paste-2-run.log" },
      ],
    });
    expect(promptBox().value).toBe("");
    expect(chips()).toHaveLength(0);
    expect(note()).toContain("Saved to the memo queue");

    // The NEXT prompt, typed in the same dialog, is kept as a draft like any other, and the
    // stashed files do not come back with it.
    await type("次の作業");
    await reopen();
    expect(promptBox().value).toBe("次の作業");
    expect(chips()).toHaveLength(0);
  });

  it("creates no memo and keeps the draft when an upload fails", async () => {
    pasteMock.mockResolvedValueOnce({ status: 413 });
    await render();
    await type("残すべき本文");
    await paste(new File(["IMG"], "huge.png", { type: "image/png" }));

    await click(stashBtn());

    expect(createMock).not.toHaveBeenCalled();
    expect(promptBox().value).toBe("残すべき本文");
    expect(chips()).toHaveLength(1);
    expect(note()).toContain("too large");
  });

  it("keeps the draft when the memo could not be created", async () => {
    createMock.mockResolvedValueOnce({ error: { code: "x" } });
    await render();
    await type("残すべき本文");
    await click(stashBtn());
    expect(promptBox().value).toBe("残すべき本文");
    expect(note()).toContain("Could not save");
  });
});

describe("Save to memo while it runs", () => {
  // What was saved is what gets cleared, so nothing may change meanwhile (review of #3293).
  it("locks typing, attaching and launching until the save is done", async () => {
    let release!: () => void;
    createMock.mockImplementationOnce(() => new Promise((r) => (release = () => r({ id: "m1" }))));
    await render();
    await type("保存する本文");
    await click(stashBtn());
    expect(promptBox().readOnly).toBe(true);
    expect(byText("Start in a worktree").disabled).toBe(true);
    await paste(new File(["X"], "late.png", { type: "image/png" }));
    expect(chips()).toHaveLength(0);
    await act(async () => release());
    await settle();
    expect(promptBox().readOnly).toBe(false);
    expect(promptBox().value).toBe("");
    expect(onLaunch).not.toHaveBeenCalled();
  });
});

describe("Start from a memo", () => {
  it("stages the memo's files as chips and hands them to the launch", async () => {
    stored = {
      "paste-1.png": { body: "IMG", type: "image/png" },
      "paste-2-run.log": { body: "LOG", type: "application/octet-stream" },
    };
    await render({
      initialPrompt: "メモの本文",
      initialAttachments: [
        { path: "/x/paste-1.png", name: "paste-1.png" },
        { path: "/x/paste-2-run.log", name: "paste-2-run.log" },
      ],
    });
    expect(promptBox().value).toBe("メモの本文");
    expect(chips()).toHaveLength(2);
    expect(chips()[0].querySelector("img.ma-thumb")?.getAttribute("src")).toMatch(/^blob:/);
    expect(chips()[1].textContent).toContain("run.log"); // the original name, not paste-2-…

    await click(byText("Start in a worktree"));
    const files = (onLaunch.mock.calls[0][0] as LaunchOpts).images;
    expect(files.map((f) => f.name)).toEqual(["paste-1.png", "run.log"]);
    expect(await files[1].text()).toBe("LOG");
  });

  // A launch before the files arrived would go out without them, and then delete the memo
  // that still had them (review of #3293).
  it("cannot launch or save until the memo's files are staged", async () => {
    stored = { "paste-1.png": { body: "IMG", type: "image/png" } };
    let release!: () => void;
    const gate = new Promise<void>((r) => (release = r));
    blockFetch = gate;
    await render({ initialPrompt: "本文", initialAttachments: [{ path: "/x/paste-1.png", name: "paste-1.png" }] });
    expect(chips()).toHaveLength(0);
    expect(byText("Start in a worktree").disabled).toBe(true);
    expect(stashBtn().disabled).toBe(true);
    await act(async () => release());
    await settle();
    expect(chips()).toHaveLength(1);
    expect(byText("Start in a worktree").disabled).toBe(false);
  });

  it("says how many files could not be loaded", async () => {
    stored = { "paste-1.png": { body: "IMG", type: "image/png" } };
    await render({
      initialAttachments: [
        { path: "/x/paste-1.png", name: "paste-1.png" },
        { path: "/x/paste-9.png", name: "paste-9.png" },
      ],
    });
    expect(chips()).toHaveLength(1);
    expect(note()).toContain("1 of the memo's attachments");
  });
});
