// The jump's side: where each stop leads, and when the walk may continue.
//
// A report's content lives in the operator conversation, not the reporting session, so an
// unread report must open the conversation — opening the session showed nothing new and its
// on-screen acknowledgement cleared the report unseen. And the walk continues only while the
// layout is the one the previous jump left: a navigation of the user's own starts afresh.
import { beforeEach, describe, expect, it, vi } from "vitest";

const apiJSON = vi.fn(async (..._args: unknown[]) => ({}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
const opened: string[] = [];
vi.mock("./open.ts", () => ({
  openSessionFromList: (s: { name: string }) => {
    opened.push("session:" + s.name);
    return true;
  },
}));
vi.mock("../notifications/store.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  openNotificationTarget: async (n: { payload: { conversation_id: string } }) => {
    opened.push("conversation:" + n.payload.conversation_id);
    return { opened: true };
  },
}));

const { jumpToNextAttention } = await import("./attentionJump.ts");
const { useNotificationStore } = await import("../notifications/store.ts");
const { useSessionsStore } = await import("./store.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
type FleetNotification = import("../notifications/store.ts").FleetNotification;
type Layout = import("../../layout/types.ts").Layout;

const note = (id: string, session: string, kind: string, createdAt: string, payload: Record<string, unknown> = {}): FleetNotification => ({
  seq: Number(id.replace(/\D/g, "")), id, kind, target: { type: "session", id: session },
  displayName: session, payload, createdAt, seen: false,
});
const emptyLayout = (): Layout => ({
  version: 3, mode: "split", cols: [{ id: "c0", rowRatio: 0.5, cells: [{ id: "g0", selectedViewId: null, views: [] }] }],
  colRatios: [1], activeCellId: "g0",
});

beforeEach(() => {
  opened.length = 0;
  apiJSON.mockClear();
  useLayoutStore.setState({ layout: emptyLayout() });
});

describe("jumpToNextAttention", () => {
  it("opens an unread report's conversation and acknowledges it, and a waiting session itself", async () => {
    useSessionsStore.setState({
      sessions: [
        { name: "asks", kind: "claude", alive: true, state: "question" },
        { name: "reported", kind: "claude", alive: true, state: "" },
      ],
    });
    useNotificationStore.setState({
      items: [note("e1", "reported", "session-report", "2026-09-27T10:00:00Z", { conversation_id: "conv-1" })],
    });
    await jumpToNextAttention();
    await jumpToNextAttention();
    expect(opened).toEqual(["session:asks", "conversation:conv-1"]);
    const seen = apiJSON.mock.calls.filter((c) => c[0] === "api/notifications/seen").map((c) => (c[2] as { eventIds?: string[] }).eventIds);
    expect(seen).toEqual([["e1"]]);
  });

  it("opens the session for an unread finished turn", async () => {
    useSessionsStore.setState({ sessions: [{ name: "done", kind: "claude", alive: true, state: "" }] });
    useNotificationStore.setState({ items: [note("e2", "done", "answer-ready", "2026-09-27T10:00:00Z")] });
    await jumpToNextAttention();
    expect(opened).toEqual(["session:done"]);
  });

  it("starts a fresh walk once the user has navigated in between", async () => {
    useSessionsStore.setState({
      sessions: [
        { name: "a", kind: "claude", alive: true, state: "question" },
        { name: "b", kind: "claude", alive: true, state: "question" },
        { name: "c", kind: "claude", alive: true, state: "question" },
      ],
    });
    useNotificationStore.setState({ items: [] });
    await jumpToNextAttention(); // the mocked open leaves the layout untouched → walk continues
    await jumpToNextAttention();
    expect(opened).toEqual(["session:a", "session:b"]);
    // Any layout change of the user's own (here: a fresh layout showing nothing) resets it.
    useLayoutStore.setState({ layout: emptyLayout() });
    await jumpToNextAttention();
    expect(opened.at(-1)).toBe("session:a");
  });
});
