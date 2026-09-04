// A session report's destination is the conversation (docs/log/30). A notification lives 7 days
// in the Control Plane while the conversation can disappear, either because the user deleted it
// or because a test dropped a ghost notification into the real HOME
// (workspace/agent/internal/chatx/chat_report.go, interimDeliveries). The contract: when it is
// gone, fall back to the reporting session and state the reason.
//
// The point: a failed fetch does not mean the conversation is gone. Reading the 5xx right after a
// WS start, or a dropped connection, as "gone" would divert everyone who clicked during the boot
// window to the session even though the conversation is alive.
//
// This lives in the dom project because store.ts pulls in core/api/client.ts, which touches
// localStorage at import time and cannot even be imported in the node environment.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { conversationReachable, openNotificationTarget, type FleetNotification } from "./store.ts";
import { chatGet } from "../../core/api/client.ts";
import { openChat } from "../chat/open.ts";
import { openSessionChat } from "../sessions/open.ts";
import { useSessionsStore } from "../sessions/store.ts";

vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  chatGet: vi.fn(),
}));
vi.mock("../chat/open.ts", () => ({ openChat: vi.fn(), openChatSplit: vi.fn(), openAssistantDraft: vi.fn() }));
vi.mock("../sessions/open.ts", () => ({
  openSessionChat: vi.fn(), openSessionChatSplit: vi.fn(),
  openSessionTerminal: vi.fn(), openSessionTerminalSplit: vi.fn(),
}));

describe("deciding whether a report's conversation is alive", () => {
  it("opens the conversation when the fetch succeeds", () => {
    expect(conversationReachable({ id: "c1", messages: [] })).toBe(true);
  });

  it("does not read a transient failure (5xx during WS start, dropped connection) as gone", () => {
    expect(conversationReachable({ error: { code: "http_502" } })).toBe(true);
    expect(conversationReachable({ error: { code: "internal", status: 500 } })).toBe(true);
    expect(conversationReachable(null)).toBe(true); // the fetch threw
  });

  it("only a 404 (chat_conversation_not_found) means gone", () => {
    expect(conversationReachable({ error: { code: "chat_conversation_not_found", status: 404 } })).toBe(false);
  });
});

describe("where clicking a session report leads", () => {
  const report = (): FleetNotification => ({
    seq: 1, id: "e1", kind: "session-report",
    target: { type: "session", id: "s1" },
    displayName: "プラン検証",
    payload: { conversation_id: "c1", conversationTitle: "運用オペレーター" },
    createdAt: "2026-09-04T00:43:00Z", seen: false,
  });

  beforeEach(() => {
    vi.clearAllMocks();
    useSessionsStore.setState({ sessions: [{ name: "s1", kind: "claude", alive: true }] });
  });

  it("opens the conversation while it is alive", async () => {
    vi.mocked(chatGet).mockResolvedValue({ id: "c1" } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true });
    expect(openChat).toHaveBeenCalledWith("c1");
    expect(openSessionChat).not.toHaveBeenCalled();
  });

  it("falls back to the reporting session and returns the conversation name when it is gone", async () => {
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "chat_conversation_not_found", status: 404 } } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true, missingConversation: "運用オペレーター" });
    // Must not dead-end: no red "conversation not found" pane.
    expect(openChat).not.toHaveBeenCalled();
    expect(openSessionChat).toHaveBeenCalledWith("s1");
  });

  it("still reports the missing conversation when neither conversation nor session exists", async () => {
    useSessionsStore.setState({ sessions: [] });
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "chat_conversation_not_found", status: 404 } } as never);
    const r = await openNotificationTarget(report(), false);
    expect(r.opened).toBe(false);
    expect(r.missingConversation).toBe("運用オペレーター");
  });

  it("opens the conversation on a transient failure during WS start, without diverting to the session", async () => {
    vi.mocked(chatGet).mockResolvedValue({ error: { code: "http_502" } } as never);
    expect(await openNotificationTarget(report(), false)).toEqual({ opened: true });
    expect(openChat).toHaveBeenCalledWith("c1");
  });
});
