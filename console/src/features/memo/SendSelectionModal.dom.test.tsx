// Where a note queued from a file's send dialog lands. A worktree folder is never a bucket
// of its own: the memo is filed under the base repository so a queue set on the base's
// category sees it. A regression is silent — the memo is created, in a bucket nobody set up.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Memo, MemoInput } from "../../types/memo.ts";
import type { Repo } from "../repos/store.ts";

const created: MemoInput[] = [];
vi.mock("./api.ts", () => ({
  memoList: async () => [],
  memoCreate: async (input: MemoInput) => {
    created.push(input);
    return { id: "new", ...input } as unknown as Memo;
  },
}));
vi.mock("../chat/api.ts", () => ({ assistantList: async () => ({ assistants: [] }), chatCreate: vi.fn() }));

const { SendSelectionModal } = await import("./SendSelectionModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useReposStore } = await import("../repos/store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function open(filePath: string) {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <SendSelectionModal filePath={filePath} quote="q" startLine={1} endLine={2} onClose={() => {}} />
      </ToastProvider>,
    );
  });
}

async function queue() {
  const btn = [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => /Add to queue|キュー/.test(b.textContent || ""));
  if (!btn) throw new Error("no queue button: " + document.body.textContent);
  await act(async () => btn.click());
}

beforeEach(() => {
  created.length = 0;
  localStorage.clear();
  useReposStore.setState({ repos: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
});

describe("SendSelectionModal filing", () => {
  it("files a note from a worktree's file under the base repository", async () => {
    useReposStore.setState({
      repos: [{ name: "app@one", worktree: true, parent: "app" } as unknown as Repo, { name: "app" } as unknown as Repo],
    });
    await open("repos/app@one/src/a.ts");
    await queue();
    expect(created.map((c) => c.repo)).toEqual(["app"]);
  });

  it("falls back to the folder name's base when the repo record is gone", async () => {
    await open("repos/app@one/src/a.ts");
    await queue();
    expect(created.map((c) => c.repo)).toEqual(["app"]);
  });

  it("leaves a base clone and a file outside repos alone", async () => {
    await open("repos/app/src/a.ts");
    await queue();
    await open("notes/a.md");
    await queue();
    expect(created.map((c) => c.repo)).toEqual(["app", ""]);
  });
});
