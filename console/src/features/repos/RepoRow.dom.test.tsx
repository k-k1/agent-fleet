// Render tests for the working-copy row's identity affordances. A worktree row is
// labelled by its BRANCH, so the folder it actually lives in is only readable from
// the tooltip and the「ディレクトリ名をコピー」menu item — both are checked here.
// The right-click menu also must not re-list every agent kind: 起動 (modal) and the
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
import type { Repo } from "./store.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
let root: Root | null = null;
let host: HTMLDivElement;

const WT: Repo = { name: "app@wip-x", path: "/home/dev/repos/app@wip-x", branch: "temp/x", worktree: true, parent: "app" };

async function render(r: Repo): Promise<void> {
  await act(async () => {
    root!.render(
      <RepoRow
        r={r}
        kinds={["claude", "codex", "shell"]}
        onOpen={() => {}}
        onLaunch={() => {}}
        onStartWork={async () => ({ ok: true }) as never}
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
  // ロケールは settings 由来（既定は環境依存）なので、文言で検証する以上ここで固定する。
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

describe("RepoRow の worktree 行", () => {
  it("ブランチ名を表示しつつ、ツールチップにディレクトリ名を出す", async () => {
    await render(WT);
    expect(host.querySelector(".repo-name")!.textContent).toContain("temp/x");
    expect(host.querySelector<HTMLElement>(".repo-card")!.title).toContain("ディレクトリ: app@wip-x");
    expect(host.querySelector<HTMLElement>(".repo-name")!.title).toContain("ディレクトリ: app@wip-x");
  });

  it("ディレクトリ名のコピーは repos からの相対パス（＝フォルダ名）", async () => {
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

  it("右クリックメニューに残る起動項目は shell だけ", async () => {
    await render(WT);
    await openMenu();
    expect(itemFor("Shell を起動")).toBeTruthy();
    expect(itemFor("Claude を起動")).toBeUndefined();
    expect(itemFor("Codex を起動")).toBeUndefined();
  });
});
