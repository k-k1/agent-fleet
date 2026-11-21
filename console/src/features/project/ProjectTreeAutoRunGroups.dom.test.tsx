// Render test for the rail's Auto-run / Schedules groups (issue #4627): worktrees raised by
// memo auto-run (origin queue) or a schedule fold into a group under their repository, with
// a count and a running badge, while the member's own copies stay in the tree. The fold
// persists, and a filter match inside a folded group forces it open.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Repo } from "../repos/store.ts";
import type { Session } from "../../types/session.ts";

let served: Repo[] = [];

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  api: vi.fn(async (url: string) => {
    if (url.startsWith("api/repos")) return { repos: served };
    if (url.startsWith("api/connections")) return {};
    return {};
  }),
  isTransientErr: () => false,
  getTenant: () => "",
  getUser: () => "",
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


const REPOS: Repo[] = [
  { name: "af", branch: "develop" },
  wt("af@hand", "af", at(1)),
  wt("af@run", "af", at(2)),
  wt("af@rev", "af", at(3)),
  wt("af@cron", "af", at(4)),
];
const SESSIONS: Session[] = [
  sess("handwork", { repo: "af@hand", createdAt: at(1), origin: "user" }),
  sess("nightly-run", { repo: "af@run", createdAt: at(2), origin: "queue" }),
  sess("run-reviewer", { repo: "af@rev", createdAt: at(3), origin: "session", originSession: "nightly-run" }),
  sess("cron-job", { repo: "af@cron", createdAt: at(4), origin: "schedule", alive: false }),
];

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

const group = (kind: string) => host.querySelector<HTMLLIElement>(`li.proj-group[data-proj-group="${kind}"]`);
const head = (kind: string) => group(kind)!.querySelector<HTMLButtonElement>(":scope > .proj-group-head")!;
const copies = () =>
  [...host.querySelectorAll("li.proj-node [data-rail-repo]")].map((e) => e.getAttribute("data-rail-repo"));

beforeEach(() => {
  localStorage.clear();
  useWorkspaceStore.setState({ state: "running" });
  useProjectFilter.setState({ q: "" });
  served = REPOS;
  useReposStore.setState({ repos: REPOS });
  useSessionsStore.setState({ sessions: SESSIONS });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("auto-run groups in the repository tree", () => {
  it("folds unattended copies away by default and keeps the member's own in the tree", async () => {
    await render();
    expect(copies()).toEqual(["af", "af@hand"]);
    expect(head("autorun").getAttribute("aria-expanded")).toBe("false");
    expect(head("schedule").getAttribute("aria-expanded")).toBe("false");
  });

  it("shows a count, and a running badge only while something in the group is live", async () => {
    await render();
    // af@run + its reviewer af@rev, both alive.
    expect(head("autorun").querySelector(".repo-sess-badge:not(.run)")!.textContent).toBe("2");
    expect(head("autorun").querySelector(".repo-sess-badge.run")!.textContent).toBe("●2");
    // The schedule's only session is stopped: a count, no running badge.
    expect(head("schedule").querySelector(".repo-sess-badge:not(.run)")!.textContent).toBe("1");
    expect(head("schedule").querySelector(".repo-sess-badge.run")).toBeNull();
  });

  it("opens on click with the run's reviewer nested under it, and remembers the choice", async () => {
    await render();
    await act(async () => head("autorun").click());
    expect(copies()).toEqual(["af", "af@hand", "af@run", "af@rev"]);
    expect(
      group("autorun")!.querySelector(':scope > ul.proj-children > li.proj-node > ul.proj-children [data-rail-repo="af@rev"]'),
    ).not.toBeNull();
    // A remount reads the persisted fold.
    await act(async () => root!.unmount());
    root = createRoot(host);
    await render();
    expect(head("autorun").getAttribute("aria-expanded")).toBe("true");
    expect(head("schedule").getAttribute("aria-expanded")).toBe("false");
  });

  it("forces a group open while filtering to a session inside it, and hides the rest", async () => {
    useProjectFilter.setState({ q: "run-reviewer" });
    await render();
    expect(head("autorun").getAttribute("aria-expanded")).toBe("true");
    expect(group("schedule")).toBeNull();
    expect(copies()).toEqual(["af", "af@run", "af@rev"]);
  });
});
