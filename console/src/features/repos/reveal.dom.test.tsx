// openRepoMenuInRail (issue #1557) against the real project tree: the session menu's
// "Menu of repository" lands on the row's own context menu, after the reveal has unfolded the
// way down to it. Driven through ProjectTree rather than a stand-in row, because the timing
// (the row mounts only after the expand, and is focused a frame later) is the whole point.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Repo } from "./store.ts";

let served: Repo[] = [];
vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => {
    if (url.startsWith("api/repos")) return { repos: served };
    return {};
  }),
  isTransientErr: () => false,
}));

// jsdom refuses `view: window` in a MouseEvent init ("member view is not of type Window"), so
// the real synthContextMenu cannot run here. Same event, minus the view.
vi.mock("../project/contextMenuKey.ts", async (orig) => {
  const real = await orig<typeof import("../project/contextMenuKey.ts")>();
  return {
    ...real,
    synthContextMenu: (el: HTMLElement) => {
      const { x, y } = real.menuAnchor(el);
      el.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, button: 2, clientX: x, clientY: y }));
    },
  };
});

const { ProjectTree } = await import("../project/ProjectTree.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore, useRepoReveal } = await import("./store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useProjectFilter } = await import("../project/filter.ts");
const { openRepoMenuInRail } = await import("./reveal.ts");

// No sessions anywhere, so every node starts folded: the worktree's row is not in the DOM
// until the reveal opens its base.
const REPOS: Repo[] = [
  { name: "app", branch: "develop" },
  { name: "app@wip-sab", worktree: true, parent: "app", branch: "feature/x", createdAt: "2026-10-01T00:00:00Z" },
];

let root: Root;
let host: HTMLDivElement;

const render = async () => {
  await act(async () => {
    root.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectTree />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
};
const row = (name: string) => host.querySelector<HTMLElement>(`[data-rail-repo="${name}"]`);
const repoMenu = () => document.querySelector(".repo-ctxmenu");
// Let the rAF-driven reveal and the wait in openRepoMenuInRail run to the end. Flushed in
// short act() steps: one act around the whole wait would hold back the very re-render that
// mounts the row until the wait had already given up.
const settle = async (p: Promise<boolean>) => {
  let out: boolean | undefined;
  void p.then((v) => (out = v));
  for (let i = 0; i < 100 && out === undefined; i++) {
    await act(async () => {
      await new Promise((res) => setTimeout(res, 20));
    });
  }
  return out;
};

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
  useProjectFilter.setState({ q: "" });
  served = REPOS;
  useReposStore.setState({ repos: REPOS });
  useSessionsStore.setState({ sessions: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

describe("openRepoMenuInRail", () => {
  it("unfolds the base, then opens the worktree row's own menu", async () => {
    await render();
    expect(row("app@wip-sab")).toBeNull(); // folded away under its base
    expect(await settle(openRepoMenuInRail("app@wip-sab"))).toBe(true);
    expect(document.activeElement).toBe(row("app@wip-sab"));
    expect(repoMenu()).not.toBeNull();
  });

  it("opens the repository section too when it was folded", async () => {
    // Folded, the section mounts no node at all, so without this the reveal had no row to find.
    localStorage.setItem("af-section-repos", "0");
    await render();
    expect(row("app")).toBeNull();
    expect(await settle(openRepoMenuInRail("app@wip-sab"))).toBe(true);
    expect(repoMenu()).not.toBeNull();
    expect(localStorage.getItem("af-section-repos")).toBe("1");
  });

  it("does not unfold a folded section on mount because of an earlier reveal", async () => {
    useRepoReveal.getState().reveal("app"); // made before this tree mounted
    localStorage.setItem("af-section-repos", "0");
    await render();
    expect(row("app")).toBeNull();
  });

  it("gives focus back to the row when the menu closes", async () => {
    // jsdom has no layout, so offsetParent is always null and useMenuRoving would skip every
    // item; give the elements a parent so focus really moves into the menu first.
    const desc = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetParent")!;
    Object.defineProperty(HTMLElement.prototype, "offsetParent", {
      configurable: true,
      get(this: HTMLElement) {
        return this.parentElement;
      },
    });
    try {
      await render();
      expect(await settle(openRepoMenuInRail("app@wip-sab"))).toBe(true);
      expect(document.activeElement?.closest(".repo-ctxmenu")).not.toBeNull();
      await act(async () => {
        document.activeElement!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
      });
      expect(repoMenu()).toBeNull();
      expect(document.activeElement).toBe(row("app@wip-sab"));
    } finally {
      Object.defineProperty(HTMLElement.prototype, "offsetParent", desc);
    }
  });

  it("resolves false and opens nothing when the rail never shows the row", async () => {
    useProjectFilter.setState({ q: "zzz-no-match" }); // the rail search hides every repo
    await render();
    expect(await settle(openRepoMenuInRail("app@wip-sab"))).toBe(false);
    expect(repoMenu()).toBeNull();
  });
});
