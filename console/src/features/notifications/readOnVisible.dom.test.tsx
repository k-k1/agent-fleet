// Showing a session acknowledges its pending notifications. The scope used to be the ACTIVE
// pane alone, which was invisible while the only consumer was a badge count — and wrong the
// moment a dot appeared on every pane tab: a session on the selected tab of an unfocused cell
// is on screen, so wearing "nobody has looked at this" is a lie the user cannot clear without
// clicking into a pane they are already reading.
//
// The boundary is the SELECTED view of each cell (allPanes), not every view: a tab sitting in
// the background is exactly the case the dot is for.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Cell, Layout, View } from "../../layout/types.ts";

const apiJSON = vi.fn(async (..._args: unknown[]) => ({}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

const { useNotificationStore, wireNotificationReadOnVisibleSessions } = await import("./store.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { setSetting } = await import("../../lib/settings.ts");
type FleetNotification = import("./store.ts").FleetNotification;

const view = (id: string, session: string): View => ({ id, session, content: { kind: "terminal", chat: false }, wrap: null });
const cell = (id: string, views: View[], selected = views[0]?.id || null): Cell => ({ id, views, selectedViewId: selected });
const layout = (cells: Cell[]): Layout => ({
  version: 3, mode: "tabs", cols: [{ id: "c0", rowRatio: 0.5, cells }], colRatios: [1], activeCellId: cells[0].id,
});

const event = (id: string, session: string): FleetNotification => ({
  seq: Number(id.replace(/\D/g, "")), id, kind: "answer-ready", target: { type: "session", id: session },
  displayName: session, payload: {}, createdAt: "2026-09-22T00:00:00Z", seen: false,
});

/** Event ids posted to the CP as acknowledged, flattened across calls. */
const acked = (): string[] =>
  apiJSON.mock.calls
    .filter((c) => c[0] === "api/notifications/seen")
    .flatMap((c) => ((c[2] as { eventIds?: string[] })?.eventIds ?? []));

let stop: (() => void) | null = null;

beforeEach(() => {
  apiJSON.mockClear();
  useNotificationStore.setState({ items: [], maxSeq: 0, unseenCount: 0 });
});

afterEach(() => {
  stop?.();
  stop = null;
});

describe("which sessions count as looked at", () => {
  it("acknowledges every visible pane, not just the focused one", async () => {
    useNotificationStore.setState({ items: [event("e1", "focused"), event("e2", "beside")] });
    useLayoutStore.setState({
      layout: layout([cell("g1", [view("p1", "focused")]), cell("g2", [view("p2", "beside")])]),
    });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked().sort()).toEqual(["e1", "e2"]);
  });

  it("leaves a background tab unread — that is the dot's whole job", async () => {
    useNotificationStore.setState({ items: [event("e1", "front"), event("e2", "behind")] });
    useLayoutStore.setState({
      layout: layout([cell("g1", [view("p1", "front"), view("p2", "behind")], "p1")]),
    });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual(["e1"]);
  });

  it("posts one acknowledgement for a session open in two panes", async () => {
    useNotificationStore.setState({ items: [event("e1", "twice")] });
    useLayoutStore.setState({
      layout: layout([cell("g1", [view("p1", "twice")]), cell("g2", [view("p2", "twice")])]),
    });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual(["e1"]);
  });

  it("catches a notification that arrives while its session is already on screen", async () => {
    useLayoutStore.setState({ layout: layout([cell("g1", [view("p1", "open")])]) });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual([]);
    useNotificationStore.setState({ items: [event("e9", "open")] });
    await Promise.resolve();
    expect(acked()).toEqual(["e9"]);
  });
});

// childIdleNotify off: a spawned child's idle is acknowledged on arrival, wherever it is, so
// it raises no dot — its question, and any other session's idle, still wait to be looked at.
describe("a muted child's idle", () => {
  const question = (id: string, session: string): FleetNotification => ({ ...event(id, session), kind: "question" });

  beforeEach(() => {
    useLayoutStore.setState({ layout: layout([cell("g1", [view("p1", "elsewhere")])]) });
    useSessionsStore.setState({
      sessions: [
        { name: "child", kind: "claude", alive: true, origin: "session", originSession: "parent" },
        { name: "parent", kind: "claude", alive: true, origin: "user" },
      ],
    });
  });

  afterEach(() => setSetting("childIdleNotify", true));

  it("is acknowledged off screen when the setting is off", async () => {
    setSetting("childIdleNotify", false);
    useNotificationStore.setState({ items: [event("e1", "child"), question("e2", "child"), event("e3", "parent")] });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual(["e1"]);
  });

  it("keeps its dot while the setting is on", async () => {
    setSetting("childIdleNotify", true);
    useNotificationStore.setState({ items: [event("e1", "child")] });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual([]);
  });

  it("is caught once the session list says it is a child", async () => {
    setSetting("childIdleNotify", false);
    useSessionsStore.setState({ sessions: [] });
    useNotificationStore.setState({ items: [event("e1", "child")] });
    stop = wireNotificationReadOnVisibleSessions();
    await Promise.resolve();
    expect(acked()).toEqual([]);
    useSessionsStore.setState({ sessions: [{ name: "child", kind: "claude", alive: true, origin: "session" }] });
    await Promise.resolve();
    expect(acked()).toEqual(["e1"]);
  });
});
