// "Open in gallery" in the left pane's right-click menu (ADR 0080 decision 7).
//
// What is worth a test is the SHOWING, not the opening: the item belongs on a folder and on
// an image, and must stay away from everything else. Getting that wrong is invisible until
// someone right-clicks a .md and is offered a gallery of nothing. The one thing allowed to
// decide "is this an image" is lib/filemeta's imageFormat (decision 3) — a second extension
// table in the tree would disagree with the viewer and nobody would notice which one was
// wrong.
//
// The item is addressed by its icon, not its label: the label comes from the catalogue and
// a test matching on it fails the day the wording changes.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Entry {
  name: string;
  type: string;
}

let served: Record<string, Entry[]> = {};

vi.mock("../../core/api/client.ts", () => ({
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

const opened: { path: string; opts?: { focus?: string; session?: string; newPane?: boolean } }[] = [];
vi.mock("../gallery/open.ts", () => ({
  openGallery: (path: string, opts?: { focus?: string; session?: string; newPane?: boolean }) =>
    opened.push({ path, opts }),
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

/** Right-click a row and return the gallery item of the menu that opened, if it has one. */
async function menuFor(path: string): Promise<HTMLButtonElement | null> {
  const row = host.querySelector<HTMLElement>(`li[data-path="${path}"]`);
  expect(row, `row ${path} is not in the tree`).not.toBeNull();
  await act(async () => {
    row!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 5, clientY: 5 }));
  });
  return document.querySelector<HTMLButtonElement>(".files-ctxmenu .codicon-file-media")?.closest("button") ?? null;
}

beforeEach(() => {
  opened.length = 0;
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

describe("左ペインの右クリック「ギャラリーで開く」", () => {
  it("フォルダには出て、そのフォルダを開く", async () => {
    await render();
    const item = await menuFor("repos/shots");
    expect(item).not.toBeNull();
    await act(async () => {
      item!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(opened).toEqual([{ path: "repos/shots", opts: { focus: undefined, newPane: false } }]);
  });

  it("画像ファイルには出て、親フォルダをその画像に合わせて開く", async () => {
    await render();
    const item = await menuFor("repos/a.png");
    expect(item).not.toBeNull();
    await act(async () => {
      item!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(opened).toEqual([{ path: "repos", opts: { focus: "repos/a.png", newPane: false } }]);
  });

  it("画像でないファイルには出ない", async () => {
    await render();
    expect(await menuFor("repos/notes.md")).toBeNull();
  });

  it("Ctrl/⌘ を押しながらなら別のペインに開く", async () => {
    await render();
    const item = await menuFor("repos/shots");
    await act(async () => {
      item!.dispatchEvent(new MouseEvent("click", { bubbles: true, ctrlKey: true }));
    });
    expect(opened[0].opts?.newPane).toBe(true);
  });
});
