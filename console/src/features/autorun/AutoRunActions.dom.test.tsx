// The Auto-run view's row menu and "Clear finished runs": which dialog opens with what scope, and
// what the clear action archives after its confirmation.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { ConfirmProvider } from "../../ui/ConfirmProvider.tsx";
import { ToastProvider } from "../../ui/ToastProvider.tsx";
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
  DeleteCopyModal: (p: { node?: { repo: { name: string } }; nodes?: { repo: { name: string } }[] }) => (
    <div data-delete-modal={(p.nodes ?? (p.node ? [p.node] : [])).map((n) => n.repo.name).join(",")} />
  ),
}));
const confirm = vi.fn(async (_o: unknown) => true);
vi.mock("../../ui/confirmBridge.ts", async (orig) => ({ ...(await orig<object>()), askConfirm: (o: unknown) => confirm(o) }));
const rawCall = vi.fn(async (_u: string, _o?: unknown) => ({ ok: true }));
// An archive that lands: the session leaves the list, as the refresh after it would show.
const archiveLands = async (u: string) => {
  const name = u.split("/")[2];
  const { useSessionsStore: st } = await import("../sessions/store.ts");
  st.setState({ sessions: st.getState().sessions.filter((x) => x.name !== name) } as never);
  return { ok: true };
};
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
  rawCall.mockReset();
  rawCall.mockImplementation(archiveLands);
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
        <ConfirmProvider>
          <ToastProvider>
            <AutoRunSection />
          </ToastProvider>
        </ConfirmProvider>
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

describe("dialog lifetime when the last run goes", () => {
  it("keeps the same dialog instance mounted when the run list empties", async () => {
    await openMenu("run-a");
    await act(async () => item(0).click());
    const before = document.body.querySelector("[data-stop-modal]")!;
    useSessionsStore.setState({ sessions: [] } as never);
    await flush();
    expect(host.querySelector(".pane-empty")).not.toBeNull();
    expect(document.body.querySelector("[data-stop-modal]")).toBe(before);
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

  it("opens the delete dialog without waiting for the archive calls to settle", async () => {
    let release: (v: { ok: boolean }) => void = () => {};
    rawCall.mockImplementation(() => new Promise((r) => (release = r)));
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    // rev-old's archive is still pending, yet the copy of the merged run is on offer.
    expect(rawCall.mock.calls.map((c) => c[0])).toEqual(["api/sessions/rev-old/archive"]);
    expect(document.body.querySelector("[data-delete-modal]")!.getAttribute("data-delete-modal")).toBe("base@wip-o");
    expect(host.querySelector<HTMLButtonElement>(".autorun-clear")!.disabled).toBe(true);
    await act(async () => release({ ok: true }));
    await flush();
    expect(host.querySelector<HTMLButtonElement>(".autorun-clear")!.disabled).toBe(false);
  });

  it("archives at most three runs at once, each run child-first, and a failure keeps only its own ancestors", async () => {
    const old = (n: string, extra: Partial<Session>) => sess(n, { alive: false, repo: "base", createdAt: "2026-11-01T00:00:00Z", ...extra });
    const many: Session[] = [];
    for (const i of [1, 2, 3, 4]) {
      many.push(old(`r${i}`, { origin: "queue" }), old(`c${i}`, { origin: "session", originSession: `r${i}` }));
    }
    const refresh = vi.fn(async () => {});
    useSessionsStore.setState({ sessions: many, refresh } as never);
    await flush();
    const pending = new Map<string, (v: { ok: boolean; json?: () => Promise<unknown> }) => void>();
    const order: string[] = [];
    let inflight = 0;
    let max = 0;
    rawCall.mockImplementation((u: string) => {
      const name = u.split("/")[2];
      order.push(name);
      max = Math.max(max, ++inflight);
      return new Promise((r) =>
        pending.set(name, (v) => {
          inflight--;
          r(v);
        }),
      );
    });
    const settle = async (name: string, ok: boolean) => {
      await act(async () => pending.get(name)!({ ok, json: async () => ({}) }));
      await flush();
    };
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
    // Three runs start (in the list's order); the fourth waits for a free slot.
    expect(order).toHaveLength(3);
    expect(order.every((n) => n.startsWith("c"))).toBe(true);
    const [first, second, third] = order;
    const waiting = ["c1", "c2", "c3", "c4"].find((n) => !order.includes(n))!;
    const run = (c: string) => c.replace("c", "r");
    await settle(second, false); // its run must stay: the child failed
    expect(order).toEqual([first, second, third, waiting]);
    await settle(first, true);
    expect(order).toContain(run(first));
    for (const n of [run(first), third, waiting]) await settle(n, true);
    expect(order).toContain(run(third));
    expect(order).toContain(run(waiting));
    // The last two parents are still in flight, so the refresh has not run.
    expect(refresh).not.toHaveBeenCalled();
    for (const n of [run(third), run(waiting)]) await settle(n, true);
    expect(order).not.toContain(run(second));
    expect(max).toBe(3);
    for (const c of [first, third, waiting]) expect(order.indexOf(c)).toBeLessThan(order.indexOf(run(c)));
    expect(refresh).toHaveBeenCalledTimes(1);
    expect(host.querySelector<HTMLButtonElement>(".autorun-clear")!.disabled).toBe(false);
  });

  it("leaves the sessions inside a listed copy to the dialog", async () => {
    useSessionsStore.setState({
      sessions: [
        ...useSessionsStore.getState().sessions,
        sess("rev-in", { origin: "session", originSession: "run-old", alive: false, repo: "base@wip-o", createdAt: "2026-11-02T00:00:00Z" }),
      ],
    } as never);
    await flush();
    await act(async () => host.querySelector<HTMLButtonElement>(".autorun-clear")!.click());
    await flush();
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
