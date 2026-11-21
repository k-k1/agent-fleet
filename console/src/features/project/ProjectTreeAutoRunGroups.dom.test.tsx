// Render test for unattended work in the repository tree: memo auto-run (origin queue) leaves it
// for the Auto-run view and a schedule's runs (origin schedule) for the Schedules view (#4646).
// The member's own copies stay in the tree.
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

describe("unattended work in the repository tree", () => {
  it("leaves auto-run and schedule copies out of the tree", async () => {
    await render();
    expect(copies()).toEqual(["af", "af@hand"]);
    expect(host.querySelector("li.proj-group")).toBeNull();
  });

  it("does not surface a run's or a schedule's session when filtering for it", async () => {
    for (const q of ["run-reviewer", "cron-job"]) {
      useProjectFilter.setState({ q });
      await render();
      expect(copies()).toEqual([]);
    }
  });

  it("does not list an unattended session that reused the base clone's folder", async () => {
    served = [{ name: "af", branch: "develop" }];
    useReposStore.setState({ repos: served });
    useSessionsStore.setState({
      sessions: [
        sess("handwork", { repo: "af", createdAt: at(1), origin: "user" }),
        sess("nightly-run", { repo: "af", createdAt: at(2), origin: "queue" }),
        sess("nightly", { repo: "af", createdAt: at(3), origin: "schedule" }),
        sess("nightly-rev", { repo: "af", createdAt: at(4), origin: "session", originSession: "nightly" }),
      ],
    });
    await render();
    expect([...host.querySelectorAll("li.sess-row .sess-l1")].map((e) => e.textContent)).toEqual(["handwork"]);
    // Folded, the clone's row summarises only what the tree lists, and a search for an
    // unattended session's name finds nothing.
    await act(async () => host.querySelector<HTMLButtonElement>(".proj-node-caret")!.click());
    expect(host.textContent).not.toContain("nightly");
    expect(host.querySelector(".repo-sess-badge")?.textContent ?? "").not.toBe("4");
    useProjectFilter.setState({ q: "nightly" });
    await render();
    expect(host.querySelector("[data-rail-repo]")).toBeNull();
  });
});
