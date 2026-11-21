// The Auto-run view's row menu and "Clear finished runs": which dialog opens with what scope, and
// what the clear action archives after its confirmation.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { Session } from "../../types/session.ts";

vi.mock("../sessions/open.ts", () => ({ openSessionChat: () => {} }));
vi.mock("../memo/api.ts", () => ({
  memoQueueList: async () => ({ tenantEnabled: true, queues: [{ categoryId: "c1", repo: "r", category: "q" }] }),
  memoQueueDeliveries: async () => [
    { id: "d1", session: "run-a", state: "open" },
    { id: "d0", session: "run-old", state: "closed", sentAt: "2026-11-01T00:00:00Z", closedAt: "2026-11-01T00:45:00Z" },
  ],
}));
vi.mock("../repos/StopSessionsModal.tsx", () => ({
  StopSessionsModal: (p: { node: { repo: { name: string } }; only?: Set<string> }) => (
    <div data-stop-modal={p.node.repo.name} data-only={[...(p.only ?? [])].sort().join(",")} />
  ),
}));
vi.mock("../repos/DeleteCopyModal.tsx", () => ({
  DeleteCopyModal: (p: { node: { repo: { name: string } } }) => <div data-delete-modal={p.node.repo.name} />,
}));
const confirm = vi.fn(async (_o: unknown) => true);
vi.mock("../../ui/confirmBridge.ts", () => ({ askConfirm: (o: unknown) => confirm(o) }));
const rawCall = vi.fn(async (_u: string, _o?: unknown) => ({ ok: true }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<object>()),
  raw: (u: string, o?: unknown) => rawCall(u, o),
  sessionDelete: async () => ({ ok: true }),
}));

const { AutoRunSection } = await import("./AutoRunSection.tsx");
const { useSessionsStore } = await import("../sessions/store.ts");
const { useReposStore } = await import("../repos/store.ts");
const { ViewShownContext } = await import("../../lib/viewShown.ts");

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root;
let host: HTMLDivElement;
const sess = (name: string, extra: Partial<Session>): Session => ({ name, title: name, kind: "claude", alive: true, ...extra });
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

beforeEach(async () => {
  confirm.mockClear();
  rawCall.mockClear();
  useReposStore.setState({
    repos: [{ name: "base" }, { name: "base@wip-a", worktree: true, parent: "base" }, { name: "base@wip-o", worktree: true, parent: "base" }],
  } as never);
  useSessionsStore.setState({
    sessions: [
      sess("run-a", { origin: "queue", repo: "base@wip-a", createdAt: new Date().toISOString() }),
      sess("rev-a", { origin: "session", originSession: "run-a", repo: "base@wip-a" }),
      sess("mine", { origin: "user", repo: "base@wip-a" }),
      sess("run-old", { origin: "queue", alive: false, repo: "base@wip-o", createdAt: "2026-11-01T00:00:00Z", pr: { number: 3, state: "merged", url: "u" } }),
      sess("rev-old", { origin: "session", originSession: "run-old", alive: false, repo: "base" }),
    ],
    refresh: async () => {},
  } as never);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root.render(
      <ViewShownContext.Provider value={true}>
        <AutoRunSection />
      </ViewShownContext.Provider>,
    ),
  );
  await flush();
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});

const openMenu = async (run: string) => {
  await act(async () => host.querySelector<HTMLButtonElement>(`[data-autorun-run="${run}"] .autorun-run-menu`)!.click());
};
const item = (i: number) => document.body.querySelectorAll<HTMLButtonElement>(".ui-menu .ui-menu-item")[i];

describe("run row menu", () => {
  it("stops only the run's sessions, never the member's own session in the same copy", async () => {
    await openMenu("run-a");
    await act(async () => item(0).click());
    const m = document.body.querySelector("[data-stop-modal]")!;
    expect(m.getAttribute("data-stop-modal")).toBe("base@wip-a");
    expect(m.getAttribute("data-only")).toBe("rev-a,run-a");
  });

  it("opens the delete dialog on the run's worktree", async () => {
    await openMenu("run-a");
    await act(async () => item(1).click());
    expect(document.body.querySelector("[data-delete-modal]")!.getAttribute("data-delete-modal")).toBe("base@wip-a");
  });

  it("offers no worktree deletion for a run in a base clone, and no stop once nothing is alive", async () => {
    useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((s) => (s.name === "run-a" ? { ...s, repo: "base" } : s)) } as never);
    await flush();
    await openMenu("run-a");
    expect(document.body.querySelectorAll(".ui-menu .ui-menu-item")).toHaveLength(1);
  });
});

describe("dialog lifetime", () => {
  it("keeps the stop dialog open when the run's sessions stop and the row moves away", async () => {
    await openMenu("run-a");
    await act(async () => item(0).click());
    useSessionsStore.setState({ sessions: useSessionsStore.getState().sessions.map((s) => (s.origin === "user" ? s : { ...s, alive: false })) } as never);
    await flush();
    expect(document.body.querySelector("[data-stop-modal]")).not.toBeNull();
  });
});

describe("Clear finished runs", () => {
  it("archives the stopped sessions that do not go with a merged worktree, after one confirmation, then offers the worktree", async () => {
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    expect(confirm).toHaveBeenCalledTimes(1);
    // rev-old sits in the base clone: archived here; run-old's merged worktree goes through the delete dialog.
    expect(rawCall.mock.calls.map((c) => c[0])).toEqual(["api/sessions/rev-old/archive"]);
    expect(document.body.querySelector("[data-delete-modal]")!.getAttribute("data-delete-modal")).toBe("base@wip-o");
  });

  it("does not offer a worktree a member's session entered while the confirmation was open", async () => {
    confirm.mockImplementationOnce(async () => {
      useSessionsStore.setState({
        sessions: [...useSessionsStore.getState().sessions, sess("late", { origin: "user", alive: false, repo: "base@wip-o", createdAt: "2026-11-05T00:00:00Z" })],
      } as never);
      return true;
    });
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    expect(document.body.querySelector("[data-delete-modal]")).toBeNull();
  });

  it("archives the run's own session only after its children succeeded", async () => {
    useSessionsStore.setState({
      sessions: useSessionsStore.getState().sessions.map((s) => (s.name === "run-old" ? { ...s, repo: "base" } : s)),
    } as never);
    await flush();
    rawCall.mockImplementation(async (u: string) => ({ ok: !u.includes("rev-old"), json: async () => ({}) }) as never);
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    expect(rawCall.mock.calls.map((c) => c[0])).toEqual(["api/sessions/rev-old/archive"]);
    rawCall.mockImplementation(async () => ({ ok: true }));
  });

  it("archives nothing when the confirmation is declined", async () => {
    confirm.mockResolvedValueOnce(false);
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    expect(rawCall).not.toHaveBeenCalled();
    expect(document.body.querySelector("[data-delete-modal]")).toBeNull();
  });
});
