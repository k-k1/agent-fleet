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

let tenantNow = "";
let served: Record<string, Entry[]> = {};

vi.mock("../../core/api/client.ts", () => ({
  getTenant: () => tenantNow,
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
let startResult: "ok" | "reject" | "switch" = "ok";
vi.mock("../repos/useStartWork.ts", () => ({
  useStartWork: () => async (target: { dir: string; repo: string }, opts: never) => {
    started.push({ target, opts });
    if (startResult === "reject") throw new Error("network");
    if (startResult === "switch") tenantNow = "other";
    return { ok: true, name: "s-new" };
  },
}));
const addedToSet: [string, string][] = [];
vi.mock("../../lib/workingSetsStore.ts", async (orig) => ({
  ...(await orig<object>()),
  autoAddToActiveWorkingSet: (kind: string, id: string) => addedToSet.push([kind, id]),
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
  addedToSet.length = 0;
  startResult = "ok";
  tenantNow = "";
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false } });
  useReposStore.setState({ repos: [], loadState: "ok" });
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


async function clickShell(path: string): Promise<void> {
  await render();
  const item = await menuFor(path);
  expect(item).not.toBeNull();
  await act(async () => {
    item!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

describe("左ペインの右クリック「ここでシェルを開く」", () => {
  it("作業コピー内のフォルダは、コピーを dir にして起動し、グループへは足さない", async () => {
    useReposStore.setState({ repos: [{ name: "shots" }], loadState: "ok" });
    await clickShell("repos/shots");
    expect(started).toHaveLength(1);
    expect(started[0].target).toEqual({ dir: "repos/shots", repo: "" });
    expect(started[0].opts).toMatchObject({ kind: "shell", worktree: false, subdir: "" });
    expect(started[0].opts.title).toContain("shots");
    expect(addedToSet).toEqual([]);
  });

  it("作業コピーに属さないフォルダは、そのパスを dir にして起動し、選択中のグループへ足す", async () => {
    await clickShell("repos/docs");
    expect(started[0].target).toEqual({ dir: "repos/docs", repo: "" });
    expect(started[0].opts.subdir).toBe("");
    expect(addedToSet).toEqual([["sessions", "s-new"]]);
  });

  it("起動が例外で落ちても未処理にせず、失敗を通知する", async () => {
    startResult = "reject";
    await clickShell("repos/docs");
    expect(document.body.textContent).toContain("Launch failed");
    expect(addedToSet).toEqual([]);
  });

  it("作業コピー一覧が未取得なら、コピー外と決め打ちせず起動しないで通知する", async () => {
    useReposStore.setState({ repos: [], loadState: "unreachable" });
    await clickShell("repos/docs");
    expect(started).toHaveLength(0);
    expect(document.body.textContent).toContain("Launch failed");
  });

  it("一覧の取得待ち中にテナントを切り替えたら、別テナントへは起動しない", async () => {
    useReposStore.setState({ repos: [], loadState: "unreachable" });
    const real = useReposStore.getState().refresh;
    useReposStore.setState({
      refresh: async () => {
        tenantNow = "other";
        useReposStore.setState({ loadState: "ok" });
        return true;
      },
    });
    try {
      await clickShell("repos/docs");
    } finally {
      useReposStore.setState({ refresh: real });
    }
    expect(started).toHaveLength(0);
  });

  it("作成の応答待ち中にテナントを切り替えたら、別テナントのグループへは足さない", async () => {
    await clickShell("repos/docs");
    expect(addedToSet).toHaveLength(1);
    addedToSet.length = 0;
    startResult = "switch";
    await clickShell("repos/docs");
    expect(started).toHaveLength(2);
    expect(addedToSet).toEqual([]);
  });

  it("ファイルには出ない", async () => {
    await render();
    expect(await menuFor("repos/notes.md")).toBeNull();
    expect(started).toHaveLength(0);
  });
});
