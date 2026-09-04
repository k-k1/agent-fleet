// Render tests for the 変更 list's row menu (right-click / ⋯): the three entries
// — 差分 / 表示 / 編集 — and what each one opens.
//
// The row click already opened the diff before the menu existed; these cover the
// wiring that is easy to break: the ⋯ button must NOT also trigger the row's
// diff, 表示 / 編集 open the file itself (the home-relative path, not the
// repo-relative one the diff takes), and a deleted file offers the diff only.
// Plus the one exception to diff-on-click — an untracked file, which has no
// working diff to show and so opens the file view instead. The last block covers
// the auto-refresh at the end of a session's turn (差し替えるだけ・失敗しても残す).
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
// 自動更新の検証用: 次の api 応答を過渡的失敗にする／応答を手で止める。
let failing = false;
let gate: { wait: Promise<void>; open: () => void } | null = null;

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async () => {
    if (gate) await gate.wait;
    if (failing) return { error: { code: "http_502", status: 502 } };
    return { changes: served };
  }),
  isTransientErr: (d: unknown) => {
    const err = (d as { error?: { code?: string; status?: number } } | null)?.error;
    return !!err && ((typeof err.status === "number" && err.status >= 500) || /^http_5\d\d$/.test(err.code || ""));
  },
}));

const openFileDiff = vi.fn();
const openFileMode = vi.fn();
vi.mock("../scm/open.ts", () => ({ openFileDiff: (...a: unknown[]) => openFileDiff(...a) }));
vi.mock("../viewer/openFile.ts", () => ({ openFileMode: (...a: unknown[]) => openFileMode(...a) }));

const { FilesChanges } = await import("./FilesChanges.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useFilesStore } = await import("../files/store.ts");

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
/** Each group's band, as [project, branch] — branch "" when it fell back to the folder. */
const bands = () =>
  [...host.querySelectorAll<HTMLDivElement>(".chg-repo")].map((b) => [
    b.querySelector(".wc-project")?.textContent ?? "",
    b.querySelector(".wc-branch-name")?.textContent ?? "",
  ]);
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
  useReposStore.setState({ repos: [] });
  useFilesStore.setState({ tick: 0, scoped: { prefix: "", n: 0 } });
  failing = false;
  gate = null;
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

  it("still opens the diff of a staged change, and offers every entry for an untracked file", async () => {
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

  // git has no working diff for an untracked file, so the row click that opens
  // one everywhere else would land the reader on an empty diff pane. It opens
  // the file itself instead — by the home-relative path the file view takes.
  it("opens the file view, not the diff, on a click on an untracked row", async () => {
    served = [
      { path: "repos/demo/new.ts", repo: "demo", untracked: true },
      { path: "repos/demo/src/a.ts", repo: "demo", worktree: "M", index: " " },
    ];
    await render();
    await fire(rows()[0], "click");
    expect(openFileMode).toHaveBeenCalledWith("repos/demo/new.ts", "view");
    expect(openFileDiff).not.toHaveBeenCalled();

    // The tracked neighbour keeps the diff-on-click behavior.
    await fire(rows()[1], "click");
    expect(openFileDiff).toHaveBeenCalledWith("demo", "src/a.ts", false);
  });

  // The 差分 entry stays reachable from the menu for an untracked file — the row
  // click changed, the menu did not.
  it("still opens the diff of an untracked file from the menu", async () => {
    served = [{ path: "repos/demo/new.ts", repo: "demo", untracked: true }];
    await render();
    await fire(rows()[0], "contextmenu");
    await fire(menuItems()[0], "click");
    expect(openFileDiff).toHaveBeenCalledWith("demo", "new.ts", false);
  });

  it("offers the diff only for a deleted file", async () => {
    served = [{ path: "repos/demo/gone.ts", repo: "demo", worktree: "D", index: " " }];
    await render();
    await fire(rows()[0], "contextmenu");
    expect(menuItems().map((b) => b.disabled)).toEqual([false, true, true]);
  });
});

// The band over each group names the working copy as プロジェクト + ブランチ, not
// as the "<base>@<slug>" folder the API groups by — a wip slug tells a reader
// nothing about which line of work the changes belong to.
describe("working-copy group bands", () => {
  it("titles a worktree group with its base project and branch", async () => {
    useReposStore.setState({
      repos: [
        { name: "agent-fleet", branch: "develop" },
        { name: "agent-fleet@wip-a", worktree: true, parent: "agent-fleet", branch: "temp/aaa" },
      ],
    });
    served = [{ path: "repos/agent-fleet@wip-a/x.ts", repo: "agent-fleet@wip-a", worktree: "M", index: " " }];
    await render();
    expect(bands()).toEqual([["agent-fleet", "temp/aaa"]]);
    // The folder stays reachable — it is still the identity behind the label.
    expect(host.querySelector(".chg-repo")?.getAttribute("title")).toBe("agent-fleet@wip-a");
  });

  it("falls back to the folder when the branch is unknown (SVN copy, or repo not loaded)", async () => {
    useReposStore.setState({ repos: [{ name: "svn-wc", vcs: "svn", revision: "42" }] });
    served = [
      { path: "repos/svn-wc/x.ts", repo: "svn-wc", worktree: "M", index: " " },
      { path: "repos/unknown/y.ts", repo: "unknown", worktree: "M", index: " " },
    ];
    await render();
    expect(bands()).toEqual([
      ["svn-wc", ""],
      ["unknown", ""],
    ]);
  });

  it("orders the groups like the rail: base first, then its worktrees oldest-first", async () => {
    useReposStore.setState({
      repos: [
        { name: "zzz", branch: "main" },
        { name: "af@wip-new", worktree: true, parent: "af", branch: "temp/new", createdAt: "2026-07-02T00:00:00Z" },
        { name: "af", branch: "develop" },
        { name: "af@wip-old", worktree: true, parent: "af", branch: "temp/old", createdAt: "2026-07-01T00:00:00Z" },
      ],
    });
    // Served in directory order — the API's order, which scatters the project.
    served = [
      { path: "repos/af@wip-new/a.ts", repo: "af@wip-new", worktree: "M", index: " " },
      { path: "repos/zzz/b.ts", repo: "zzz", worktree: "M", index: " " },
      { path: "repos/af/c.ts", repo: "af", worktree: "M", index: " " },
      { path: "repos/af@wip-old/d.ts", repo: "af@wip-old", worktree: "M", index: " " },
    ];
    await render();
    expect(bands()).toEqual([
      ["af", "develop"],
      ["af", "temp/old"],
      ["af", "temp/new"],
      ["zzz", "main"],
    ]);
  });
});

// セッションが入力待ちに入ったときの自動更新（features/files/sessionRefresh.ts の合図）。
// この一覧は「エージェントが何を触ったか」を見る面なので、ターンの終わりに追いつくのが
// 本来の姿。ただし読み直しのたびに「読み込み中」へ戻ると、勝手に点滅しているようにしか
// 見えない — 差し替えるだけであることを固定する。
describe("ターン終了時の自動更新", () => {
  const turnEnded = async () => {
    await act(async () => {
      useFilesStore.getState().refreshUnder("repos/demo");
    });
    await act(async () => {
      await Promise.resolve();
    });
  };

  it("一覧を消さずに差し替える（読み込み中に戻さない）", async () => {
    await render();
    expect(rows()).toHaveLength(1);

    // 応答を止めたまま合図を出す: 読み直しの最中も古い一覧が出ていること。
    let open!: () => void;
    gate = { wait: new Promise<void>((r) => (open = r)), open: () => open() };
    served = [
      { path: "repos/demo/src/a.ts", repo: "demo", worktree: "M", index: " " },
      { path: "repos/demo/src/new.ts", repo: "demo", untracked: true },
    ];
    await turnEnded();
    expect(rows()).toHaveLength(1); // 「読み込み中」に落ちていない
    expect(host.querySelector(".ui-empty")).toBeNull();

    gate.open();
    gate = null;
    await act(async () => {
      await Promise.resolve();
    });
    expect(rows()).toHaveLength(2); // 差し替わった
  });

  it("読み直しが 502 で失敗しても、今の一覧を残す", async () => {
    await render();
    expect(rows()).toHaveLength(1);
    failing = true;
    await turnEnded();
    expect(rows()).toHaveLength(1);
    expect(host.querySelector(".ui-empty")).toBeNull();
  });
});
