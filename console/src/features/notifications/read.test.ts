// destinationShown decides whether a notification's destination is on screen — the rule the
// OS-notification suppression and the attention jump share. A report is on screen in its
// conversation, not in its session: suppressing it there would leave it silent AND unread.
import { describe, expect, it } from "vitest";
import { destinationShown } from "./read.ts";
import type { FleetNotification } from "./store.ts";
import type { View } from "../../layout/types.ts";

const n = (kind: string, payload: Record<string, unknown> = {}): FleetNotification => ({
  seq: 1, id: "e1", kind, target: { type: "session", id: "worker" }, displayName: "worker", payload, createdAt: "2026-09-27T10:00:00Z", seen: false,
});
const term = (session: string): View => ({ id: "p1", session, content: { kind: "terminal", chat: true }, wrap: null });
const chat = (conversationId: string): View => ({ id: "p1", session: null, content: { kind: "chat", conversationId, draftAssistantId: null }, wrap: null });
const sessions = [{ name: "worker" }];

describe("destinationShown", () => {
  it("puts a finished turn in its session", () => {
    expect(destinationShown(n("answer-ready"), term("worker"), sessions)).toBe(true);
    expect(destinationShown(n("answer-ready"), term("other"), sessions)).toBe(false);
  });

  it("puts a report in its conversation, not in the reporting session", () => {
    const report = n("session-report", { conversation_id: "conv-1" });
    expect(destinationShown(report, term("worker"), sessions)).toBe(false);
    expect(destinationShown(report, chat("conv-1"), sessions)).toBe(true);
    expect(destinationShown(report, chat("conv-2"), sessions)).toBe(false);
  });

  it("finds nothing on screen without a pane", () => {
    expect(destinationShown(n("answer-ready"), undefined, sessions)).toBe(false);
  });
});
