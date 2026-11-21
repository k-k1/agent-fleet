// The activity bar (ADR 0140): which icon is pressed, what a click does on a desktop and on a
// phone, and each badge rule against the stores it reads.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const { api, memoList } = vi.hoisted(() => ({
  api: vi.fn((..._args: unknown[]) => Promise.resolve({})),
  memoList: vi.fn((): Promise<unknown> => Promise.resolve([])),
}));
vi.mock("../core/api/client.ts", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  api: (...args: unknown[]) => api(...args),
  memoList: () => memoList(),
}));

import { ActivityBar } from "./ActivityBar.tsx";
import { RailView as RailViewPane } from "./RailView.tsx";
import { SharedSessionsSection } from "../features/sharing/SharedSessionsSection.tsx";
import { useRailAck } from "./activity/badges.ts";
import { useLeftRail } from "../core/store/leftRail.ts";
import type { RailView } from "../core/store/leftRail.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { useNotificationStore } from "../features/notifications/store.ts";
import type { FleetNotification } from "../features/notifications/store.ts";
import { useWorkItemStore } from "../features/workitems/store.ts";
import { useSeenStore } from "../features/workitems/seen.ts";
import { useAutoRunStore } from "../features/autorun/store.ts";
import type { RunDelivery } from "../features/autorun/runs.ts";
import { useTenantStore } from "../core/store/tenant.ts";
import { useSchedulesStore } from "../features/schedules/store.ts";
import { useLayoutStore } from "../layout/store.ts";
import { useWorkspaceStore } from "../core/store/workspace.ts";
import { useHandoffStore } from "../features/sharing/handoffStore.ts";
import type { HandoffOffer } from "../features/sharing/handoffStore.ts";
import { useMemoStore } from "../features/memo/store.ts";
import { useMemoryStore } from "../features/memory/store.ts";
import { tCount } from "../lib/i18n/index.ts";
import { selectedView } from "../layout/ops.ts";
import type { Session } from "../types/session.ts";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;
let root: Root | null = null;
let host: HTMLDivElement | null = null;

const sess = (name: string, state: string) => ({ name, state, alive: true, kind: "claude" }) as unknown as Session;
const note = (over: Partial<FleetNotification>): FleetNotification => ({
  seq: 1,
  id: "n1",
  kind: "answer-ready",
  target: { type: "session", id: "s1" },
  displayName: "",
  payload: {},
  createdAt: "",
  seen: false,
  ...over,
});
const workItems = (updatedAt: string) => ({
  payload: {
    items: [{ id: "1", queryId: "q", provider: "github", kind: "issue", key: "o/r#1", title: "t", state: "open", url: "", assignee: "", labels: [], labelColors: {}, updatedAt, commentCount: 0 }],
    queries: [],
    sessions: [],
    fetchedAt: "",
    running: true,
  },
});

beforeEach(() => {
  localStorage.clear();
  useLeftRail.setState({ open: true, view: "sessions", swipeOverlay: false });
  useSessionsStore.setState({ sessions: [] });
  useNotificationStore.setState({ items: [] });
  useWorkItemStore.setState({ payload: null });
  useSeenStore.setState({ map: {} });
  useSchedulesStore.setState({ failed: [], failedAll: null });
  useRailAck.setState({ acked: {} });
  useAutoRunStore.setState({ queues: [], deliveries: new Map(), loaded: false, scope: "" });
  useMemoryStore.getState().reset();
  useHandoffStore.setState({ owned: [], received: [], receivedAt: 0 });
  useMemoStore.setState({ tick: 0, panels: 0, waiting: null, scope: "" });
  memoList.mockReset();
  memoList.mockImplementation(() => Promise.resolve([]));
  api.mockImplementation(() => Promise.resolve({}));
});
afterEach(async () => {
  if (root) await act(async () => root!.unmount());
  host?.remove();
  root = host = null;
});

async function mount(props: Partial<Parameters<typeof ActivityBar>[0]> = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await render(props);
}
async function render(props: Partial<Parameters<typeof ActivityBar>[0]> = {}) {
  // The shell passes the store's view down; mirror that so a click re-renders like the app.
  const Shell = () => {
    const view = useLeftRail((s) => s.view);
    const open = useLeftRail((s) => s.open);
    return <ActivityBar view={view} shown={open} foldable schedulerEnabled {...props} />;
  };
  await act(async () => root!.render(<Shell />));
}
const btn = (view: RailView) => host!.querySelector<HTMLButtonElement>(`.actbar-btn[data-rail-view="${view}"]`)!;
const badge = (view: RailView) => btn(view).querySelector<HTMLElement>(".actbar-badge");

describe("ActivityBar shared view", () => {
  const offer = (id: string, status: string) => ({ id, status }) as unknown as HandoffOffer;

  it("places the shared icon right after Sessions", async () => {
    await mount();
    const ids = [...host!.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")].map((b) => b.dataset.railView);
    expect(ids.slice(0, 2)).toEqual(["sessions", "shared"]);
  });

  it("badges it with the handoffs waiting for a decision, and with nothing else", async () => {
    useHandoffStore.setState({ received: [offer("a", "pending"), offer("b", "pending"), offer("c", "accepted")] });
    await mount();
    expect(badge("shared")?.dataset.badge).toBe("waiting");
    expect(badge("shared")?.textContent).toBe("2");
    expect(btn("shared").title).toContain(tCount("rail.badge.handoffs", 2));
    expect(badge("sessions")).toBeNull();
  });

  it("badges from the CP's answer while another view is on screen, before the shared view was ever shown", async () => {
    api.mockImplementation((...a: unknown[]) =>
      Promise.resolve(a[0] === "api/session-handoff-offers/received" ? { offers: [offer("a", "pending")] } : {}),
    );
    useLeftRail.setState({ view: "assistant" });
    await mount();
    // What App does: the section is mounted inside a hidden view for the whole session.
    const extra = document.createElement("div");
    document.body.append(extra);
    const r2 = createRoot(extra);
    await act(async () =>
      r2.render(
        <RailViewPane id="shared" active="assistant" shown>
          <SharedSessionsSection />
        </RailViewPane>,
      ),
    );
    await act(async () => void (await new Promise((r) => setTimeout(r, 5))));
    expect(badge("shared")?.textContent).toBe("1");
    await act(async () => r2.unmount());
    extra.remove();
  });

  it("shows no badge without a pending handoff", async () => {
    useHandoffStore.setState({ received: [offer("c", "declined")] });
    await mount();
    expect(badge("shared")).toBeNull();
  });
});

describe("ActivityBar auto-run view", () => {
  const run = (name: string, state: string, extra: Partial<Session> = {}) => ({ name, state, alive: true, kind: "claude", origin: "queue", ...extra }) as unknown as Session;
  // outcome is the delivery's; turn is how its first turn ended (the only failure signal a queue records).
  const dlv = (id: string, state: string, outcome = "", turn = ""): RunDelivery => ({ id, categoryId: "c", state, outcome, turnOutcome: turn, sentAt: "", createdAt: "", closedAt: "", heldBy: "" });
  const scopeNow = () => useTenantStore.getState().tenant + "\u0000" + useTenantStore.getState().identityRev;
  const served = (entries: [string, RunDelivery][], loaded = true) => useAutoRunStore.setState({ deliveries: new Map(entries), loaded, scope: scopeNow() });

  it("places the auto-run icon right after Memo queue, with no badge while nothing runs", async () => {
    await mount();
    const ids = [...host!.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")].map((b) => b.dataset.railView);
    expect(ids[ids.indexOf("memo") + 1]).toBe("autorun");
    expect(badge("autorun")).toBeNull();
  });

  it("counts the runs whose session waits for an answer, and leaves them out of the Sessions badge", async () => {
    useSessionsStore.setState({ sessions: [run("a", "question"), run("b", "working"), run("c", "plan"), sess("mine", "working")] });
    served([["a", dlv("d1", "open")], ["b", dlv("d2", "open")], ["c", dlv("d3", "open")]]);
    await mount();
    expect(badge("autorun")?.dataset.badge).toBe("waiting");
    expect(badge("autorun")?.textContent).toBe("2");
    expect(btn("autorun").title).toContain(tCount("rail.badge.runs", 2));
    expect(badge("sessions")).toBeNull();
    // Waiting stays while the view is open: looking at the row does not answer the session.
    await act(async () => btn("autorun").click());
    expect(badge("autorun")?.textContent).toBe("2");
  });

  it("marks a failed run with a red dot until the view is opened, then keeps it acknowledged", async () => {
    useSessionsStore.setState({ sessions: [run("a", "working", { alive: false })] });
    served([["a", dlv("d1", "closed", "clean", "turn-failed")]]);
    await mount();
    expect(badge("autorun")?.dataset.badge).toBe("failed");
    await act(async () => btn("autorun").click());
    expect(badge("autorun")).toBeNull();
    await act(async () => btn("sessions").click());
    expect(badge("autorun")).toBeNull();
    // A run the member stopped is not a failure.
    served([["a", dlv("d1", "closed", "ended", "turn-failed")], ["b", dlv("d2", "closed", "aborted", "turn-failed")], ["c", dlv("d3", "closed", "clean", "clean")]]);
    useRailAck.setState({ acked: {} });
    await render();
    expect(badge("autorun")).toBeNull();
  });

  it("shows no failed dot before the runs have been read", async () => {
    useSessionsStore.setState({ sessions: [run("a", "working", { alive: false })] });
    served([["a", dlv("d1", "closed", "clean", "turn-failed")]], false);
    await mount();
    expect(badge("autorun")).toBeNull();
  });

  it("keeps an acknowledged failure acknowledged across a reload whose runs have not arrived", async () => {
    useSessionsStore.setState({ sessions: [run("a", "working", { alive: false })] });
    useRailAck.setState({ acked: { autorun: ["d1"] } });
    useLeftRail.setState({ open: true, view: "autorun" });
    served([], true); // the queue list came back, the delivery history has not
    await mount();
    expect(useRailAck.getState().acked.autorun).toEqual(["d1"]);
    served([["a", dlv("d1", "closed", "clean", "turn-failed")]]);
    await act(async () => btn("sessions").click());
    expect(badge("autorun")).toBeNull();
  });

  it("does not show another scope's runs", async () => {
    // The delivery says the run waits for the member (held by a person); the session itself works.
    useSessionsStore.setState({ sessions: [run("a", "working")] });
    served([["a", { ...dlv("d1", "open"), heldBy: "person" }]]);
    await mount();
    expect(badge("autorun")?.dataset.badge).toBe("waiting");
    await act(async () => useAutoRunStore.setState({ scope: "someone else" }));
    expect(badge("autorun")).toBeNull();
  });

  it("counts a stopped scheduled session that still carries its question, as the list shows it", async () => {
    useSessionsStore.setState({ sessions: [run("cron", "working", { origin: "schedule", alive: false, carried: "question" })] });
    await mount();
    expect(badge("schedules")?.textContent).toBe("1");
  });

  it("counts a scheduled run's waiting session on the Schedules icon", async () => {
    useSessionsStore.setState({ sessions: [run("cron", "question", { origin: "schedule" })] });
    await mount();
    expect(badge("schedules")?.dataset.badge).toBe("waiting");
    expect(badge("sessions")).toBeNull();
  });
});

describe("ActivityBar views", () => {
  it("presses the icon of the view on screen and no other", async () => {
    await mount();
    expect(btn("sessions").getAttribute("aria-pressed")).toBe("true");
    expect(btn("files").getAttribute("aria-pressed")).toBe("false");
  });

  it("switches view on a click, and folds the pane on a click of the view on screen", async () => {
    await mount();
    await act(async () => btn("files").click());
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "files" });
    expect(btn("files").getAttribute("aria-pressed")).toBe("true");
    await act(async () => btn("files").click());
    expect(useLeftRail.getState().open).toBe(false);
    // Folded: nothing is on screen, so nothing is pressed.
    expect(btn("files").getAttribute("aria-pressed")).toBe("false");
  });

  it("only ever selects on a phone, where the drawer has no folded pane", async () => {
    await mount({ foldable: false });
    await act(async () => btn("sessions").click());
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
  });

  it("drops the schedules icon where there is no scheduler", async () => {
    await mount({ schedulerEnabled: false });
    expect(host!.querySelector('.actbar-btn[data-rail-view="schedules"]')).toBeNull();
    expect(btn("memo")).not.toBeNull();
  });

  it("opens Home in a pane without touching the left pane", async () => {
    await mount();
    await act(async () => host!.querySelector<HTMLButtonElement>('.actbar-btn[data-launch="home"]')!.click());
    expect(useLeftRail.getState()).toMatchObject({ open: true, view: "sessions" });
    const layout = useLayoutStore.getState().layout;
    const cell = layout.cols.flatMap((c) => c.cells).find((c) => c.id === layout.activeCellId)!;
    expect(selectedView(cell)?.content.kind).toBe("home");
  });
});

describe("ActivityBar badges", () => {
  it("counts sessions waiting for an answer, even while their view is on screen", async () => {
    useSessionsStore.setState({ sessions: [sess("a", "question"), sess("b", "plan"), sess("c", "working")] });
    await mount();
    expect(badge("sessions")?.dataset.badge).toBe("waiting");
    expect(badge("sessions")?.textContent).toBe("2");
  });

  it("falls back to an unread dot when a session has news but nobody is waiting", async () => {
    useNotificationStore.setState({ items: [note({})] });
    await mount();
    expect(badge("sessions")?.dataset.badge).toBe("unread");
    expect(badge("sessions")?.textContent).toBe("");
  });

  it("marks the assistant for an unseen notice that opens a conversation", async () => {
    useNotificationStore.setState({ items: [note({ kind: "session-report", payload: { conversation_id: "c1" } })] });
    await mount();
    expect(badge("assistant")?.dataset.badge).toBe("unread");
  });

  it("counts work items that moved, and acknowledges them once their view is opened", async () => {
    useSeenStore.setState({ map: { "github:o/r#1": { u: "2026-01-01T00:00:00Z", c: 0 } } });
    useWorkItemStore.setState(workItems("2026-01-02T00:00:00Z") as never);
    await mount();
    expect(badge("workitems")?.textContent).toBe("1");
    await act(async () => btn("workitems").click());
    expect(badge("workitems")).toBeNull();
    // Back on another view: what was on screen stays acknowledged…
    await act(async () => btn("sessions").click());
    expect(badge("workitems")).toBeNull();
    // …until the row moves again.
    await act(async () => useWorkItemStore.setState(workItems("2026-01-03T00:00:00Z") as never));
    expect(badge("workitems")?.textContent).toBe("1");
  });

  it("does not acknowledge news while the pane is folded away", async () => {
    useLeftRail.setState({ open: false, view: "workitems" });
    useSeenStore.setState({ map: { "github:o/r#1": { u: "2026-01-01T00:00:00Z", c: 0 } } });
    useWorkItemStore.setState(workItems("2026-01-02T00:00:00Z") as never);
    await mount();
    expect(badge("workitems")?.textContent).toBe("1");
  });

  it("marks schedules whose latest run failed until their view is opened", async () => {
    useSchedulesStore.setState({ failed: ["s1@2026-01-01T00:00:00Z"], failedAll: ["s1@2026-01-01T00:00:00Z"] });
    await mount();
    expect(badge("schedules")?.dataset.badge).toBe("failed");
    await act(async () => btn("schedules").click());
    await act(async () => btn("sessions").click());
    expect(badge("schedules")).toBeNull();
  });

  it("keeps a failure acknowledged while a working set hides it", async () => {
    const a = "a@2026-01-01T00:00:00Z";
    const b = "b@2026-01-01T00:00:00Z";
    // All: both failures listed and seen.
    useSchedulesStore.setState({ failed: [a, b], failedAll: [a, b] });
    await mount();
    await act(async () => btn("schedules").click());
    // A working set that lists only A, with the view still open.
    await act(async () => useSchedulesStore.setState({ failed: [a], failedAll: [a, b] }));
    // Another view, then back to All: B has not run again, so there is nothing new to tell.
    await act(async () => btn("sessions").click());
    await act(async () => useSchedulesStore.setState({ failed: [a, b], failedAll: [a, b] }));
    expect(badge("schedules")).toBeNull();
    // A new failure of B is news.
    const b2 = "b@2026-01-02T00:00:00Z";
    await act(async () => useSchedulesStore.setState({ failed: [a, b2], failedAll: [a, b2] }));
    expect(badge("schedules")?.dataset.badge).toBe("failed");
  });

  it("does not forget acknowledged failures on a reload, before the list has loaded", async () => {
    const a = "a@2026-01-01T00:00:00Z";
    const b = "b@2026-01-01T00:00:00Z";
    // A reload with the schedules view selected, both failures acknowledged earlier, and the
    // list not loaded yet.
    useLeftRail.setState({ open: true, view: "schedules" });
    useRailAck.setState({ acked: { schedules: [a, b] } });
    await mount();
    expect(useRailAck.getState().acked.schedules).toEqual([a, b]);
    // The list arrives under a working set that lists only A.
    await act(async () => useSchedulesStore.setState({ failed: [a], failedAll: [a, b] }));
    await act(async () => btn("sessions").click());
    await act(async () => useSchedulesStore.setState({ failed: [a, b], failedAll: [a, b] }));
    expect(badge("schedules")).toBeNull();
  });

  it("does not forget acknowledged work items before their list has loaded", async () => {
    useLeftRail.setState({ open: true, view: "workitems" });
    useRailAck.setState({ acked: { workitems: ["k"] } });
    await mount();
    expect(useRailAck.getState().acked.workitems).toEqual(["k"]);
  });

  it("counts the memory proposals waiting for a decision, even while the view is on screen (ADR 0141)", async () => {
    const prop = (id: string, status: string) => ({ id, status, memory: "m-" + id, items: [], created: "", expires: "" });
    api.mockImplementation((url: unknown) =>
      Promise.resolve(
        String(url) === "api/agents/memory-consolidation/proposals"
          ? { proposals: [prop("a", "pending"), prop("b", "pending"), prop("c", "approved")] }
          : {},
      ),
    );
    useWorkspaceStore.setState({ state: "running" });
    useLeftRail.setState({ view: "memory" });
    await mount();
    await act(async () => {});
    expect(badge("memory")?.dataset.badge).toBe("waiting");
    expect(badge("memory")?.textContent).toBe("2");
    // The memory badge speaks of suggestions, not of sessions waiting for an answer.
    expect(btn("memory").title).toContain(tCount("rail.badge.proposals", 2));
    expect(btn("memory").title).not.toContain(tCount("rail.badge.waiting", 2));
  });

  it("reads no proposals while the workspace is stopped", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await mount();
    expect(api.mock.calls.some((c) => String(c[0]).includes("memory-consolidation"))).toBe(false);
    expect(badge("memory")).toBeNull();
  });

  // The bar's read starts a tick after mount (a panel may take it over), so give it one.
  const settleReads = () => act(async () => void (await new Promise((r) => setTimeout(r, 5))));
  const note1 = (id: string, state: string, extra: Record<string, unknown> = {}) => ({ id, state, sentAt: "", ...extra });

  it("badges the memo icon with the notes waiting for approval, with the panel closed or open", async () => {
    memoList.mockImplementation(() =>
      Promise.resolve([note1("a", "held"), note1("b", "held", { heldReason: { reason: "deferred" } }), note1("c", "pending"), note1("d", ""), note1("e", "delivered")]),
    );
    useWorkspaceStore.setState({ state: "running" });
    useLeftRail.setState({ view: "memo" });
    await mount();
    await settleReads();
    expect(badge("memo")?.dataset.badge).toBe("waiting");
    expect(badge("memo")?.textContent).toBe("2");
    expect(btn("memo").title).toContain(tCount("rail.badge.memos", 2));
    expect(btn("memo").title).not.toContain(tCount("rail.badge.waiting", 2));
  });

  it("shows no memo badge before the first read and clears it once the held notes are approved", async () => {
    let list: unknown[] = [note1("a", "held")];
    let answer: () => void = () => {};
    memoList.mockImplementationOnce(() => new Promise((res) => (answer = () => res(list))));
    memoList.mockImplementation(() => Promise.resolve(list));
    useWorkspaceStore.setState({ state: "running" });
    await mount();
    await act(async () => void (await new Promise((r) => setTimeout(r, 5)))); // the read is out, unanswered
    expect(memoList).toHaveBeenCalledTimes(1);
    expect(badge("memo")).toBeNull(); // read not answered yet: not "0", not a flash
    await act(async () => answer());
    expect(badge("memo")?.textContent).toBe("1");
    list = [note1("a", "pending")];
    await act(async () => useMemoStore.getState().bump()); // the panel's write re-reads
    await settleReads();
    expect(badge("memo")).toBeNull();
  });

  it("reads no memos while the workspace is stopped", async () => {
    useWorkspaceStore.setState({ state: "stopped" });
    await mount();
    expect(memoList).not.toHaveBeenCalled();
    expect(badge("memo")).toBeNull();
  });

  it("keeps the last count when a read fails", async () => {
    memoList.mockImplementationOnce(() => Promise.resolve([note1("a", "held")]));
    useWorkspaceStore.setState({ state: "running" });
    await mount();
    await settleReads();
    memoList.mockImplementation(() => Promise.resolve({ error: "http_502" }));
    await act(async () => useMemoStore.getState().bump());
    await settleReads();
    expect(badge("memo")?.textContent).toBe("1");
  });

  it("never badges the files view", async () => {
    useSessionsStore.setState({ sessions: [sess("a", "question")] });
    await mount();
    expect(badge("files")).toBeNull();
  });
});

describe("ActivityBar sparkle mark", () => {
  it("is carried by the views that list agents at work, and by no other", async () => {
    await mount();
    const marked = [...host!.querySelectorAll<HTMLElement>(".actbar-btn[data-rail-view]")]
      .filter((b) => b.querySelector(".actbar-mark.codicon-sparkle"))
      .map((b) => b.dataset.railView);
    expect(marked).toEqual(["sessions", "shared", "autorun", "schedules"]);
  });
});
