// "Download as zip" in the left pane's folder menu (ADR 0111), and the touch long-press that
// reaches it on iOS, where a long press of a row raises no native contextmenu.
//
// The download itself is folderZip's own test. What is pinned here is the wiring a screenshot
// cannot show: the item belongs on folders only, it hands over the folder's own path, and a
// long press opens the menu WITHOUT the lift toggling the folder it was pressed on.
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
const downloadFolderZip = vi.fn(async (_path: string, _notify: unknown) => {});
vi.mock("../files/folderZip.ts", () => ({ downloadFolderZip: (p: string, n: unknown) => downloadFolderZip(p, n) }));

const { ProjectFiles } = await import("./ProjectFiles.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");
const { setLocale } = await import("../../lib/i18n/index.ts");

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

const row = (path: string) => {
  const el = host.querySelector<HTMLElement>(`li[data-path="${path}"]`);
  expect(el, `row ${path} is not in the tree`).not.toBeNull();
  return el!;
};
const menu = () => document.querySelector(".files-ctxmenu");
const zipItem = () => [...document.querySelectorAll<HTMLButtonElement>(".files-ctxmenu button")].find((b) => b.textContent?.includes("Download as zip"));

async function rightClick(path: string) {
  await act(async () => {
    row(path).dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: 5, clientY: 5 }));
  });
}

const touch = (el: Element, type: string, x = 20, y = 20) => {
  const e = new Event(type, { bubbles: true, cancelable: true });
  Object.defineProperty(e, "touches", { value: type === "touchend" || type === "touchcancel" ? [] : [{ clientX: x, clientY: y }] });
  el.dispatchEvent(e);
  return e;
};
const pointer = (el: Element, pointerType: string) => {
  const e = new Event("pointerdown", { bubbles: true });
  Object.defineProperty(e, "pointerType", { value: pointerType });
  el.dispatchEvent(e);
};

beforeEach(() => {
  setLocale("en");
  downloadFolderZip.mockClear();
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false } });
  useReposStore.setState({ repos: [] });
  served = {
    repos: [
      { name: "shots", type: "dir" },
      { name: "docs", type: "dir" },
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
  vi.useRealTimers();
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
});

describe("左ペインの右クリック「Download as zip」", () => {
  it("フォルダには出て、そのフォルダのパスで zip を頼む", async () => {
    await render();
    await rightClick("repos/shots");
    const item = zipItem();
    expect(item).toBeDefined();
    expect(item!.title).toContain("node_modules");
    await act(async () => {
      item!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(downloadFolderZip).toHaveBeenCalledTimes(1);
    expect(downloadFolderZip.mock.calls[0][0]).toBe("repos/shots");
    expect(menu()).toBeNull(); // closed like every other item
  });

  it("ファイルには出ない（ファイルの「ダウンロード」はそのまま）", async () => {
    await render();
    await rightClick("repos/notes.md");
    expect(menu()).not.toBeNull();
    expect(zipItem()).toBeUndefined();
    expect(document.querySelector(".files-ctxmenu a[download]")).not.toBeNull();
  });
});

describe("左ペインのフォルダ行のタッチ長押し", () => {
  const hold = async (el: Element, ms: number) => {
    await act(async () => {
      pointer(el, "touch");
      touch(el, "touchstart");
      vi.advanceTimersByTime(ms);
    });
  };

  it("500ms 押し続けるとメニューが開き、指を離してもフォルダは開閉しない", async () => {
    await render();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const r = row("repos/shots");
    expect(r.getAttribute("aria-expanded")).toBe("false");
    await hold(r, 520);
    expect(menu()).not.toBeNull();
    expect(zipItem()).toBeDefined();
    let lift!: Event;
    await act(async () => {
      lift = touch(r, "touchend");
      r.dispatchEvent(new MouseEvent("click", { bubbles: true })); // sent anyway by some browsers
    });
    expect(lift.defaultPrevented).toBe(true);
    expect(row("repos/shots").getAttribute("aria-expanded")).toBe("false");
    expect(menu()).not.toBeNull();
  });

  it("その後の普通のタップは今までどおりフォルダを開く", async () => {
    await render();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const r = row("repos/shots");
    await hold(r, 520);
    await act(async () => {
      touch(r, "touchend");
      vi.advanceTimersByTime(1000); // the swallow window closes
    });
    await act(async () => {
      document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    });
    expect(menu()).toBeNull();
    await act(async () => {
      r.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(row("repos/shots").getAttribute("aria-expanded")).toBe("true");
  });

  it("指が動いたら（スクロール）長押しにしない", async () => {
    await render();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const r = row("repos/shots");
    await act(async () => {
      pointer(r, "touch");
      touch(r, "touchstart", 20, 20);
      touch(r, "touchmove", 20, 70);
      vi.advanceTimersByTime(800);
    });
    expect(menu()).toBeNull();
  });

  it("touchcancel で取り消される", async () => {
    await render();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const r = row("repos/shots");
    await act(async () => {
      pointer(r, "touch");
      touch(r, "touchstart");
      touch(r, "touchcancel");
      vi.advanceTimersByTime(800);
    });
    expect(menu()).toBeNull();
  });

  it("ファイル行は長押ししてもメニューを出さない（フォルダの面だけ）", async () => {
    await render();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    await hold(row("repos/notes.md"), 700);
    expect(menu()).toBeNull();
  });
});
