// A new notification is not raised as an OS notification when its destination is already in
// the active pane. For a report that destination is its conversation: the reporting session on
// screen shows none of it (and no longer acknowledges it), so suppressing it there would leave
// the report silent and unread (#1057 review).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Layout, View } from "../../layout/types.ts";

const raised: string[] = [];
class FakeNotification {
  static permission = "granted";
  onclick: (() => void) | null = null;
  constructor(_title: string, opts: { tag: string }) {
    raised.push(opts.tag);
  }
  close() {}
}

const { useNotificationStore } = await import("./store.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
type FleetNotification = import("./store.ts").FleetNotification;

const report: FleetNotification = {
  seq: 2, id: "r1", kind: "session-report", target: { type: "session", id: "worker" },
  displayName: "worker", payload: { conversation_id: "conv-1" }, createdAt: "2026-09-27T10:00:00Z", seen: false,
};
const layoutShowing = (view: View): Layout => ({
  version: 3, mode: "split", colRatios: [1], activeCellId: "g0",
  cols: [{ id: "c0", rowRatio: 0.5, cells: [{ id: "g0", selectedViewId: view.id, views: [view] }] }],
});

/** Loads an empty first page, then delivers `n` as a newer row. */
const arrive = (n: FleetNotification) => {
  useNotificationStore.getState().reset();
  useNotificationStore.getState().applyPayload({ items: [], maxSeq: 1, sourceState: "ready" });
  useNotificationStore.getState().applyPayload({ items: [n], maxSeq: n.seq, sourceState: "ready" });
};

beforeEach(() => {
  raised.length = 0;
  vi.stubGlobal("Notification", FakeNotification);
  useSessionsStore.setState({ sessions: [{ name: "worker", kind: "claude", alive: true }] });
});
afterEach(() => vi.unstubAllGlobals());

describe("OS notification suppression", () => {
  it("still raises a report while only its reporting session is in the active pane", () => {
    useLayoutStore.setState({ layout: layoutShowing({ id: "p1", session: "worker", content: { kind: "terminal", chat: true }, wrap: null }) });
    arrive(report);
    expect(raised).toEqual(["r1"]);
  });

  it("suppresses a report while its conversation is in the active pane", () => {
    useLayoutStore.setState({ layout: layoutShowing({ id: "p1", session: null, content: { kind: "chat", conversationId: "conv-1", draftAssistantId: null }, wrap: null }) });
    arrive(report);
    expect(raised).toEqual([]);
  });

  it("suppresses a finished turn while its session is in the active pane", () => {
    useLayoutStore.setState({ layout: layoutShowing({ id: "p1", session: "worker", content: { kind: "terminal", chat: true }, wrap: null }) });
    arrive({ ...report, id: "a1", kind: "answer-ready", payload: {} });
    expect(raised).toEqual([]);
  });
});
