// Render test for the repos tree's working-copy rows: a worktree folder is named
// "<base>@<slug>", which never says which branch it has checked out, so the row
// appends that branch as muted supplementary text next to the folder name. A base
// clone's folder IS the project, so it stays as it was (no suffix).
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
  downloadURL: vi.fn(),
  fsMkdir: vi.fn(),
  fsNewFile: vi.fn(),
  fsRename: vi.fn(),
  fsDelete: vi.fn(),
  fsSearch: vi.fn(async () => ({ hits: [] })),
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
          <ProjectFiles root="repos" markRepos />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

/** Each top-level row as [folder name, branch suffix] ("" when it has none). */
const rows = () =>
  [...host.querySelectorAll<HTMLLIElement>(".fsrow")].map((li) => [
    li.querySelector(".fs-name")?.textContent ?? "",
    li.querySelector(".fs-branch")?.textContent ?? "",
  ]);

// The reveal walk fetches each ancestor and lands its row a frame or two later.
async function flushFrames(): Promise<void> {
  for (let i = 0; i < 8; i++) {
    await act(async () => {
      await new Promise((r) => requestAnimationFrame(() => r(null)));
    });
  }
}

beforeEach(() => {
  useWorkspaceStore.setState({ state: "running" });
  useFilesStore.setState({ reveal: { path: null, n: 0, focus: false } });
  useReposStore.setState({
    repos: [
      { name: "agent-fleet", branch: "develop" },
      { name: "agent-fleet@wip-a", worktree: true, parent: "agent-fleet", branch: "temp/aaa" },
      { name: "svn-wc", vcs: "svn", revision: "42" },
    ],
  });
  served = {
    // Two entries, so the folder is not folded into a passthrough row (a/b).
    "repos/agent-fleet": [
      { name: "docs", type: "dir" },
      { name: "README.md", type: "file" },
    ],
    "repos/agent-fleet/docs": [{ name: "a.md", type: "file" }],
    repos: [
      { name: "agent-fleet", type: "dir" },
      { name: "agent-fleet@wip-a", type: "dir" },
      { name: "svn-wc", type: "dir" },
      { name: "notes.md", type: "file" },
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
});

describe("working-copy rows in the repos tree", () => {
  it("shows a worktree's branch beside its folder, and nothing extra elsewhere", async () => {
    await render();
    expect(rows()).toEqual([
      ["agent-fleet", ""], // base clone: the folder already is the project
      ["agent-fleet@wip-a", "temp/aaa"], // worktree: the slug alone says nothing
      ["svn-wc", ""], // SVN checkout: no branch to show
      ["notes.md", ""], // a plain file
    ]);
  });

  it("wears the リポジトリ row's icon: root-folder for a base, git-branch for a worktree", async () => {
    await render();
    const icons = [...host.querySelectorAll<HTMLLIElement>(".fsrow")].map(
      (li) => li.querySelector(".fs-ic > span")?.className ?? "",
    );
    expect(icons[0]).toContain("codicon-root-folder");
    expect(icons[1]).toContain("codicon-git-branch");
  });
});

// Clicking a folder — in a reply, in a document, in the リポジトリ row menu — asks the
// rail to GO there. Expanding and selecting is only half of that: without focus the
// reader is looking at a row that ↑↓ do not move.
describe("reveal", () => {
  it("expands the ancestors, opens the folder, selects it and takes the keyboard", async () => {
    await render();
    await act(async () => {
      useFilesStore.getState().revealInFiles("repos/agent-fleet/docs", { focus: true });
    });
    await flushFrames();
    const row = host.querySelector<HTMLLIElement>('li[data-path="repos/agent-fleet/docs"]');
    expect(row).not.toBeNull();
    expect(row!.className).toContain("selected");
    // The folder itself is open too, so its contents are what you land on.
    expect(host.querySelector('li[data-path="repos/agent-fleet/docs/a.md"]')).not.toBeNull();
    expect(document.activeElement).toBe(host.querySelector("ul.fstree"));
  });

  it("leaves focus alone when the reveal was a side effect (a clone landing)", async () => {
    await render();
    const outside = document.createElement("input");
    document.body.appendChild(outside);
    outside.focus();
    await act(async () => {
      useFilesStore.getState().revealInFiles("repos/agent-fleet/docs");
    });
    await flushFrames();
    expect(host.querySelector('li[data-path="repos/agent-fleet/docs"]')?.className).toContain("selected");
    expect(document.activeElement).toBe(outside); // still typing wherever they were
    outside.remove();
  });
});
