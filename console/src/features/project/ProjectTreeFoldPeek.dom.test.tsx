// Render test for the folded-node summary: a collapsed repo node shows its own newest live
// session on the repo row (else its newest stopped one, dimmed); the other live ones go to the
// tally badge's tooltip. Descendant worktrees' sessions never appear there.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

let served: Repo[] = [];

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (url: string) => {
    if (url.startsWith("api/repos")) return { repos: served };
    if (url.startsWith("api/connections")) return {};
    return {};
  }),
  isTransientErr: () => false,
  getTenant: () => "",
}));

const { ProjectTree } = await import("./ProjectTree.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { useWorkspaceStore } = await import("../../core/store/workspace.ts");
const { useReposStore } = await import("../repos/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useProjectFilter } = await import("./filter.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const at = (n: number) => `2026-09-0${n}T00:00:00Z`;
const wt = (name: string, parent: string, createdAt: string): Repo => ({ name, worktree: true, parent, createdAt });
// title = name so a row can be found by the text it actually shows (displayName).
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });

const REPOS: Repo[] = [{ name: "af", branch: "develop" }, wt("af@w", "af", at(1))];

async function render(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <ProjectTree />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const peekText = (name = "af") =>
  nodeHead(name)?.querySelector(".repo-peek-title")?.textContent ?? null;
const nodeHead = (name: string) =>
  host.querySelector<HTMLElement>(`.proj-node-head:has([data-rail-repo="${name}"])`);
const badge = () => host.querySelector<HTMLElement>(".repo-sess-badge");

beforeEach(() => {
  localStorage.clear();
  localStorage.setItem("af-proj-af", "0"); // folded
  useWorkspaceStore.setState({ state: "running" });
  useProjectFilter.setState({ q: "" });
  served = REPOS;
  useReposStore.setState({ repos: REPOS });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("folded node summary", () => {
  it("shows the newest live session and lists the others on the badge", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("old", { repo: "af", createdAt: at(1) }),
        sess("new", { repo: "af", createdAt: at(3) }),
        sess("dead", { repo: "af", createdAt: at(4), alive: false }),
        sess("mid", { repo: "af", createdAt: at(2) }),
      ],
    });
    await render();
    expect(peekText()).toBe("new");
    expect(nodeHead("af")?.querySelector(".repo-peek.stopped")).toBeNull();
    expect(badge()?.title).toContain("mid\nold");
  });

  it("falls back to the newest stopped session, dimmed, when none is live", async () => {
    useSessionsStore.setState({
      sessions: [
        sess("older", { repo: "af", createdAt: at(1), alive: false }),
        sess("newer", { repo: "af", createdAt: at(2), alive: false }),
      ],
    });
    await render();
    expect(peekText()).toBe("newer");
    expect(nodeHead("af")?.querySelector(".repo-peek.stopped")).not.toBeNull();
  });

  it("shows nothing for a node whose only live session is a descendant's", async () => {
    useSessionsStore.setState({ sessions: [sess("kid", { repo: "af@w", createdAt: at(2) })] });
    await render();
    expect(peekText()).toBeNull();
  });

  it("is gone once the node is open (the rows are already on screen)", async () => {
    localStorage.setItem("af-proj-af", "1");
    useSessionsStore.setState({ sessions: [sess("a", { repo: "af", createdAt: at(1) })] });
    await render();
    expect(peekText()).toBeNull();
    expect(host.querySelectorAll("li.sess-row").length).toBe(1);
  });
});
