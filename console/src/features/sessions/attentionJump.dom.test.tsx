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
// Opening a session puts it in the active pane, as the real opener does — the walk is keyed on
// where the jump left the user.
const show = (session: string, content: object = { kind: "terminal", chat: true }): Layout => ({
  version: 3, mode: "split", colRatios: [1], activeCellId: "g0",
  cols: [{ id: "c0", rowRatio: 0.5, cells: [{ id: "g0", selectedViewId: "p1", views: [{ id: "p1", session, content, wrap: null } as never] }] }],
});
let setLayout: (l: Layout) => void = () => {};
vi.mock("./open.ts", () => ({
  openSessionFromList: (s: { name: string }) => {
    opened.push("session:" + s.name);
    setLayout(show(s.name));
    return true;
  },
}));
let conversationResult: { opened: boolean; missingConversation?: string } = { opened: true };
vi.mock("../notifications/store.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  openNotificationTarget: async (n: { payload: { conversation_id: string } }) => {
    opened.push("conversation:" + n.payload.conversation_id);
    setLayout(show("", { kind: "chat", conversationId: n.payload.conversation_id, draftAssistantId: null }));
    return conversationResult;
  },
}));

const { jumpToNextAttention, resetAttentionWalkForTest } = await import("./attentionJump.ts");
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
  conversationResult = { opened: true };
  setLayout = (layout) => useLayoutStore.setState({ layout });
  resetAttentionWalkForTest();
  useLayoutStore.setState({ layout: emptyLayout() });
});

const seenPosts = () =>
  apiJSON.mock.calls.filter((c) => c[0] === "api/notifications/seen").map((c) => (c[2] as { eventIds?: string[] }).eventIds);

describe("jumpToNextAttention", () => {
  it("opens an unread report's conversation, and a waiting session itself", async () => {
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
    // Being on screen in the conversation is what acknowledges it — not the jump.
    expect(seenPosts()).toEqual([]);
  });

  it("acknowledges a report whose conversation is gone, so it cannot hold the queue forever", async () => {
    useSessionsStore.setState({ sessions: [{ name: "reported", kind: "claude", alive: true, state: "" }] });
    useNotificationStore.setState({
      items: [note("e1", "reported", "session-report", "2026-09-27T10:00:00Z", { conversation_id: "gone" })],
    });
    conversationResult = { opened: false, missingConversation: "" };
    await jumpToNextAttention();
    expect(seenPosts()).toEqual([["e1"]]);
  });

  it("ignores a second press while the first is still opening", async () => {
    useSessionsStore.setState({
      sessions: [
        { name: "r1", kind: "claude", alive: true, state: "" },
        { name: "r2", kind: "claude", alive: true, state: "" },
      ],
    });
    useNotificationStore.setState({
      items: [
        note("e1", "r1", "session-report", "2026-09-27T11:00:00Z", { conversation_id: "c1" }),
        note("e2", "r2", "session-report", "2026-09-27T10:00:00Z", { conversation_id: "c2" }),
      ],
    });
    await Promise.all([jumpToNextAttention(), jumpToNextAttention()]);
    expect(opened).toEqual(["conversation:c1"]);
  });

  it("opens the session for an unread finished turn", async () => {
    useSessionsStore.setState({ sessions: [{ name: "done", kind: "claude", alive: true, state: "" }] });
    useNotificationStore.setState({ items: [note("e2", "done", "answer-ready", "2026-09-27T10:00:00Z")] });
    await jumpToNextAttention();
    expect(opened).toEqual(["session:done"]);
  });

  // a waits on an answer; u1 and u2 are unread and leave the queue once shown. After landing on
  // u1 a continued walk goes on to u2, while a fresh one (u1 is no longer in the queue) starts
  // over at a — so where the next press lands says which of the two happened.
  const setupWalk = async () => {
    useSessionsStore.setState({
      sessions: [
        { name: "a", kind: "claude", alive: true, state: "question" },
        { name: "u1", kind: "claude", alive: true, state: "" },
        { name: "u2", kind: "claude", alive: true, state: "" },
        { name: "x", kind: "claude", alive: true, state: "" },
      ],
    });
    useNotificationStore.setState({
      items: [note("e1", "u1", "answer-ready", "2026-09-27T11:00:00Z"), note("e2", "u2", "answer-ready", "2026-09-27T10:00:00Z")],
    });
    await jumpToNextAttention(); // a
    await jumpToNextAttention(); // u1
    useNotificationStore.setState({ items: [{ ...note("e1", "u1", "answer-ready", "2026-09-27T11:00:00Z"), seen: true }, note("e2", "u2", "answer-ready", "2026-09-27T10:00:00Z")] });
    expect(opened).toEqual(["session:a", "session:u1"]);
  };

  it("keeps walking through a change that is not navigation (a resize)", async () => {
    await setupWalk();
    useLayoutStore.setState({ layout: { ...useLayoutStore.getState().layout, colRatios: [1] } });
    await jumpToNextAttention();
    expect(opened.at(-1)).toBe("session:u2");
  });

  it("starts afresh once the user navigated away, even back to the same place", async () => {
    await setupWalk();
    setLayout(show("x"));
    setLayout(show("u1"));
    await jumpToNextAttention();
    expect(opened.at(-1)).toBe("session:a");
  });
});
