// "Open shell here" in the left pane's right-click menu (#2460). Folders get it and files do
// not; the click starts a plain shell session whose dir is the folder. Addressed by icon, not
// label, so a catalogue rewording does not break the test.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => "",
  api: vi.fn(async (url: string) => {
    const p = decodeURIComponent(new URL(url, "http://x/").searchParams.get("path") || "");
    return { entries: served[p] || [] };
  }),
  isTransientErr: () => false,
  uploadFiles: vi.fn(),
  downloadURL: vi.fn(() => "about:blank"),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ hits: [] })),
}));

const started: { target: { dir: string; repo: string }; opts: { kind: string; title: string; worktree: boolean; subdir: string } }[] = [];
vi.mock("../repos/useStartWork.ts", () => ({
  useStartWork: () => async (target: { dir: string; repo: string }, opts: never) => {
    started.push({ target, opts });
    return { ok: true };
  },
}));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectFiles root="repos" />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

/** Right-click a row and return the shell item of the menu that opened, if it has one. */
async function menuFor(path: string): Promise<HTMLButtonElement | null> {
  const row = host.querySelector<HTMLElement>(`li[data-path="${path}"]`);
  expect(row, `row ${path} is not in the tree`).not.toBeNull();
  await act(async () => {
    row!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 5, clientY: 5 }));
  });
  return document.querySelector<HTMLButtonElement>(".files-ctxmenu .codicon-terminal")?.closest("button") ?? null;
}

beforeEach(() => {
  started.length = 0;
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false } });
  useReposStore.setState({ repos: [] });
  served = {
    repos: [
      // Two entries so the folder is not folded away into a passthrough row.
      { name: "shots", type: "dir" },
      { name: "docs", type: "dir" },
      { name: "a.png", type: "file" },
      { name: "notes.md", type: "file" },
    ],
    "repos/shots": [
      { name: "one.png", type: "file" },
      { name: "two.png", type: "file" },
    ],
    "repos/docs": [
      { name: "x.md", type: "file" },
      { name: "y.md", type: "file" },
    ],
  };
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
});


describe("左ペインの右クリック「ここでシェルを開く」", () => {
  it("フォルダには出て、そのフォルダを dir にしたシェルを起動する", async () => {
    await render();
    const item = await menuFor("repos/shots");
    expect(item).not.toBeNull();
    await act(async () => {
      item!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(started).toHaveLength(1);
    expect(started[0].target).toEqual({ dir: "repos/shots", repo: "" });
    expect(started[0].opts).toMatchObject({ kind: "shell", worktree: false, subdir: "" });
    expect(started[0].opts.title).toContain("shots");
  });

  it("ファイルには出ない", async () => {
    await render();
    expect(await menuFor("repos/notes.md")).toBeNull();
    expect(started).toHaveLength(0);
  });
});
