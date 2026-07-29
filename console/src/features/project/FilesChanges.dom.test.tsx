// Render tests for the 変更 list's row menu (right-click / ⋯): the three entries
// — 差分 / 表示 / 編集 — and what each one opens.
//
// The row click already opened the diff before the menu existed; these cover the
// wiring that is easy to break: the ⋯ button must NOT also trigger the row's
// diff, 表示 / 編集 open the file itself (the home-relative path, not the
// repo-relative one the diff takes), and a deleted file offers the diff only.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

interface Change {
  path: string;
  repo: string;
  untracked?: boolean;
  index?: string;
  worktree?: string;
}

let served: Change[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async () => ({ changes: served })),
  isTransientErr: () => false,
}));

const openFileDiff = vi.fn();
const openFileMode = vi.fn();
vi.mock("../scm/open.ts", () => ({ openFileDiff: (...a: unknown[]) => openFileDiff(...a) }));
vi.mock("../viewer/openFile.ts", () => ({ openFileMode: (...a: unknown[]) => openFileMode(...a) }));

const { FilesChanges } = await import("./FilesChanges.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");

let root: Root | null = null;
let host: HTMLDivElement;

async function render(): Promise<void> {
  await act(async () => {
    root!.render(<FilesChanges />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rows = () => [...host.querySelectorAll<HTMLLIElement>(".chg-row")];
/** The menu is portaled to document.body, not into the rail. */
const menuItems = () =>
  [...document.querySelectorAll<HTMLButtonElement>(".chg-ctxmenu .ui-menu-item")];

async function fire(el: Element, type: "click" | "contextmenu"): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, clientX: 10, clientY: 20 }));
  });
}

beforeEach(() => {
  openFileDiff.mockClear();
  openFileMode.mockClear();
  useWorkspaceStore.setState({ state: "running" });
  served = [{ path: "repos/demo/src/a.ts", repo: "demo", worktree: "M", index: " " }];
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("changed-file row menu", () => {
  it("opens the diff on a plain row click", async () => {
    await render();
    await fire(rows()[0], "click");
    expect(openFileDiff).toHaveBeenCalledWith("demo", "src/a.ts", false);
    expect(menuItems()).toHaveLength(0);
  });

  it("right-click lists 差分 / 表示 / 編集 without opening the diff", async () => {
    await render();
    await fire(rows()[0], "contextmenu");
    expect(menuItems().map((b) => b.textContent?.trim())).toEqual(["Diff", "View", "Edit"]);
    expect(openFileDiff).not.toHaveBeenCalled();
  });

  it("the ⋯ button opens the same menu instead of the diff", async () => {
    await render();
    await fire(rows()[0].querySelector(".chg-menu-btn")!, "click");
    expect(menuItems()).toHaveLength(3);
    expect(openFileDiff).not.toHaveBeenCalled();
  });

  it("表示 / 編集 open the file itself, by its home-relative path", async () => {
    await render();
    await fire(rows()[0], "contextmenu");
    await fire(menuItems()[1], "click");
    expect(openFileMode).toHaveBeenCalledWith("repos/demo/src/a.ts", "view");
    expect(menuItems()).toHaveLength(0); // the menu closes behind the choice

    await fire(rows()[0], "contextmenu");
    await fire(menuItems()[2], "click");
    expect(openFileMode).toHaveBeenCalledWith("repos/demo/src/a.ts", "edit");
  });

  it("still opens the diff of a staged change, and of an untracked file", async () => {
    served = [
      { path: "repos/demo/staged.ts", repo: "demo", index: "M", worktree: " " },
      { path: "repos/demo/new.ts", repo: "demo", untracked: true },
    ];
    await render();
    await fire(rows()[0], "contextmenu");
    await fire(menuItems()[0], "click");
    expect(openFileDiff).toHaveBeenCalledWith("demo", "staged.ts", true);

    await fire(rows()[1], "contextmenu");
    expect(menuItems().every((b) => !b.disabled)).toBe(true);
  });

  it("offers the diff only for a deleted file", async () => {
    served = [{ path: "repos/demo/gone.ts", repo: "demo", worktree: "D", index: " " }];
    await render();
    await fire(rows()[0], "contextmenu");
    expect(menuItems().map((b) => b.disabled)).toEqual([false, true, true]);
  });
});
