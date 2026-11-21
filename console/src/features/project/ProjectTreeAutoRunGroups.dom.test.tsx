// Render test for unattended work in the repository tree: a schedule's worktrees fold into a
// group under their repository (issue #4627), while memo auto-run (origin queue) leaves the tree
// for the Auto-run view (#4646). The member's own copies stay in the tree.
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
const { useNotificationStore } = await import("../notifications/store.ts");

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

describe("unattended work in the repository tree", () => {
  it("leaves auto-run copies out of the tree and folds schedule copies away", async () => {
    await render();
    expect(copies()).toEqual(["af", "af@hand"]);
    expect(group("autorun")).toBeNull();
    expect(head("schedule").getAttribute("aria-expanded")).toBe("false");
  });

  it("shows a schedule group's count, and a running badge only while something in it is live", async () => {
    await render();
    // The schedule's only session is stopped: a count, no running badge.
    expect(head("schedule").querySelector(".repo-sess-badge:not(.run)")!.textContent).toBe("1");
    expect(head("schedule").querySelector(".repo-sess-badge.run")).toBeNull();
  });

  it("keeps the schedule fold across a remount", async () => {
    await render();
    await act(async () => head("schedule").click());
    expect(copies()).toEqual(["af", "af@hand", "af@cron"]);
    await act(async () => root!.unmount());
    root = createRoot(host);
    await render();
    expect(head("schedule").getAttribute("aria-expanded")).toBe("true");
  });

  it("does not surface a run's session when filtering for it", async () => {
    useProjectFilter.setState({ q: "run-reviewer" });
    await render();
    expect(group("autorun")).toBeNull();
    expect(copies()).toEqual([]);
  });

  it("does not list an auto-run's session that reused the base clone's folder", async () => {
    served = [{ name: "af", branch: "develop" }];
    useReposStore.setState({ repos: served });
    useSessionsStore.setState({
      sessions: [
        sess("handwork", { repo: "af", createdAt: at(1), origin: "user" }),
        sess("nightly-run", { repo: "af", createdAt: at(2), origin: "queue" }),
      ],
    });
    await render();
    expect([...host.querySelectorAll("li.sess-row .sess-l1")].map((e) => e.textContent)).toEqual(["handwork"]);
    expect(group("autorun")).toBeNull();
  });
});

const key = (el: Element, k: string) =>
  act(async () => {
    el.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true }));
  });

describe("sessions that reuse an existing copy", () => {
  // A schedule starts in the base clone itself (sch.Repo) — no worktree is made.
  beforeEach(() => {
    served = [{ name: "af", branch: "develop" }];
    useReposStore.setState({ repos: served });
    useSessionsStore.setState({
      sessions: [
        sess("handwork", { repo: "af", createdAt: at(1), origin: "user" }),
        sess("nightly", { repo: "af", createdAt: at(2), origin: "schedule" }),
        sess("nightly-rev", { repo: "af", createdAt: at(3), origin: "session", originSession: "nightly" }),
      ],
    });
  });
  const rows = () => [...host.querySelectorAll("li.sess-row .sess-l1")].map((e) => e.textContent);

  it("folds the schedule's sessions away from the member's own, in the base clone's folder", async () => {
    await render();
    expect(rows()).toEqual(["handwork"]);
    expect(head("schedule").querySelector(".repo-sess-badge:not(.run)")!.textContent).toBe("2");
    await act(async () => head("schedule").click());
    expect(rows().sort()).toEqual(["handwork", "nightly", "nightly-rev"]);
    expect(group("schedule")!.querySelectorAll("li.sess-row").length).toBe(2);
  });

  it("shows the unread dot on a folded group header", async () => {
    useNotificationStore.setState({
      items: [
        { seq: 1, id: "n1", kind: "answer-ready", target: { type: "session", id: "nightly" }, displayName: "", payload: {}, createdAt: "", seen: false },
      ] as never,
    });
    await render();
    expect(head("schedule").querySelector(".unread-dot")).not.toBeNull();
    await act(async () => head("schedule").click());
    expect(head("schedule").querySelector(".unread-dot")).toBeNull(); // the row carries its own now
    useNotificationStore.setState({ items: [] });
  });

  it("is reachable with the arrow keys: down lands on the header, right opens, left closes", async () => {
    await render();
    const repoRow = host.querySelector<HTMLElement>('[data-rail-repo="af"]')!;
    const list = [...host.querySelectorAll<HTMLElement>("[data-rail-row]")];
    expect(list).toContain(head("schedule"));
    head("schedule").focus();
    await key(head("schedule"), "ArrowRight");
    expect(head("schedule").getAttribute("aria-expanded")).toBe("true");
    await key(head("schedule"), "ArrowLeft");
    expect(head("schedule").getAttribute("aria-expanded")).toBe("false");
    await key(head("schedule"), "ArrowLeft"); // collapsed: step out to the owning repo row
    expect(document.activeElement).toBe(repoRow);
  });
});
