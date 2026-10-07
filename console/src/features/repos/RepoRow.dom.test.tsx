// Render tests for the working-copy row's identity affordances. A worktree row is
// labelled by its BRANCH, so the folder it actually lives in is only readable from
// the tooltip and the "copy directory name" menu item — both are checked here.
// The right-click menu also must not re-list every agent kind: the launch modal and the
// ▼ quick menu own that, and only shell — which the modal excludes — stays.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const copyText = vi.fn(async (_s: string) => true);
vi.mock("../../lib/clipboard.ts", () => ({ copyText: (s: string) => copyText(s) }));
const toast = vi.fn();
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../../core/api/client.ts", () => ({
  api: async () => ({}),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { RepoRow } = await import("./RepoRow.tsx");
import { setLocale } from "../../lib/i18n/index.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { noteImagegenStatus, _imagegenAvailability } from "../imagegen/available.ts";
import type { Repo } from "./store.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement;

const WT: Repo = { name: "app@wip-x", path: "/home/dev/repos/app@wip-x", branch: "temp/x", worktree: true, parent: "app" };

async function render(r: Repo, extra: { onGitflowInit?: () => void; onOpenChanges?: () => void } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <RepoRow
        r={r}
        kinds={["claude", "codex", "shell"]}
        onOpen={() => {}}
        onLaunch={() => {}}
        onStartWork={async () => ({ ok: true }) as never}
        {...extra}
      />,
    );
  });
}

async function openMenu(): Promise<void> {
  await act(async () => {
    host.querySelector(".repo-row")!.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true }));
  });
}

const menuItems = () => [...document.querySelectorAll<HTMLButtonElement>(".repo-ctxmenu .ui-menu-item")];
const itemFor = (label: string) => menuItems().find((b) => b.textContent?.includes(label));

beforeEach(() => {
  g.IS_REACT_ACT_ENVIRONMENT = true;
  // The locale comes from settings and its default depends on the environment; these tests
  // assert on wording, so pin it here.
  setLocale("ja");
  copyText.mockClear();
  toast.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("RepoRow worktree row", () => {
  it("shows the branch name and puts the directory in the tooltip", async () => {
    await render(WT);
    expect(host.querySelector(".repo-name")!.textContent).toContain("temp/x");
    expect(host.querySelector<HTMLElement>(".repo-card")!.title).toContain("ディレクトリ: app@wip-x");
    expect(host.querySelector<HTMLElement>(".repo-name")!.title).toContain("ディレクトリ: app@wip-x");
  });

  it("copies the directory as the path relative to repos (that is, the folder name)", async () => {
    await render(WT);
    await openMenu();
    const branchIdx = menuItems().findIndex((b) => b.textContent?.includes("ブランチ名をコピー"));
    const dirIdx = menuItems().findIndex((b) => b.textContent?.includes("ディレクトリ名をコピー"));
    expect(dirIdx).toBe(branchIdx + 1);
    await act(async () => {
      menuItems()[dirIdx].dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
    });
    expect(copyText).toHaveBeenCalledWith("app@wip-x");
  });

  it("leaves shell as the only launch item in the right-click menu", async () => {
    await render(WT);
    await openMenu();
    expect(itemFor("Shell を起動")).toBeTruthy();
    expect(itemFor("Claude を起動")).toBeUndefined();
    expect(itemFor("Codex を起動")).toBeUndefined();
  });

  it("offers to start an image studio only while the fleet has an image engine", async () => {
    await render(WT);
    await openMenu();
    expect(itemFor("画像スタジオを始める")).toBeUndefined();
    act(() => root?.unmount());
    root = createRoot(host);
    useWorkspaceStore.setState({ state: "running" });
    noteImagegenStatus({ providers: [{ id: "comfy", kind: "comfy", fleet: true, ready: true, models: [] }] } as never);
    await render(WT);
    await openMenu();
    expect(itemFor("画像スタジオを始める")).toBeTruthy();
    _imagegenAvailability.reset();
    useWorkspaceStore.setState({ state: "…" });
  });
});

describe("RepoRow Initialize Git Flow", () => {
  const CLONE: Repo = { name: "app", path: "/home/dev/repos/app", branch: "main", vcs: "git" };

  it("is offered on a git parent clone and opens the dialog", async () => {
    const open = vi.fn();
    await render(CLONE, { onGitflowInit: open });
    await openMenu();
    await act(async () => itemFor("Git Flow を初期化…")!.click());
    expect(open).toHaveBeenCalledTimes(1);
  });

  it("is not offered on a worktree or an svn working copy", async () => {
    await render(WT, { onGitflowInit: () => {} });
    await openMenu();
    expect(itemFor("Git Flow を初期化…")).toBeUndefined();
    act(() => root!.unmount());
    root = createRoot(host);
    await render({ ...CLONE, vcs: "svn" }, { onGitflowInit: () => {} });
    await openMenu();
    expect(itemFor("Git Flow を初期化…")).toBeUndefined();
  });
});

describe("RepoRow svn history entries (#1705)", () => {
  const CLONE: Repo = { name: "app", path: "/home/dev/repos/app", branch: "main", vcs: "git" };
  it("offers Show log and local changes on an svn row, and keeps the git entries off it", async () => {
    await render({ ...CLONE, vcs: "svn" }, { onOpenChanges: () => {} });
    await openMenu();
    expect(itemFor("ログを表示")).toBeDefined();
    expect(itemFor("ローカルの変更")).toBeDefined();
    expect(itemFor("コミットグラフを開く")).toBeUndefined();
    expect(itemFor("変更をコミット")).toBeUndefined();
  });

  it("leaves a git row as it was", async () => {
    await render(CLONE, { onOpenChanges: () => {} });
    await openMenu();
    expect(itemFor("コミットグラフを開く")).toBeDefined();
    expect(itemFor("変更をコミット")).toBeDefined();
    expect(itemFor("ログを表示")).toBeUndefined();
    expect(itemFor("ローカルの変更")).toBeUndefined();
  });
});

describe("RepoRow origin ahead/behind chip", () => {
  // The chip's flex gap is the only spacing between its parts; a part that falls back to
  // text-with-spaces is spaced by the font instead, which is what made the gaps uneven.
  const parts = () => [...host.querySelectorAll(".repo-chip.ab > span")].map((s) => s.textContent);

  it("renders each part as its own element with no literal spaces", async () => {
    await render({ ...WT, ahead: 4, behind: 4 });
    expect(parts()).toEqual(["↑4", "↓4", "要マージ"]);
    expect(host.querySelector(".repo-chip.ab")!.childNodes.length).toBe(3);
  });

  it("shows only the parts that apply", async () => {
    await render({ ...WT, behind: 2 });
    expect(parts()).toEqual(["↓2", "FF可"]);
    await render({ ...WT, ahead: 3 });
    expect(parts()).toEqual(["↑3"]);
  });
});
