// childIdleNotify: with it off, a spawned child (origin "session") going idle neither pops an
// OS notification nor speaks — through the CP feed (store.ts deliver). Its question, and an
// ordinary session's answer, still interrupt; the row itself stays in the center.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useNotificationStore, type FleetNotification } from "./store.ts";
import { childIdleMuted } from "./childIdle.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { setSetting } from "../../lib/settings.ts";
import { announce } from "../chat/tts.ts";

vi.mock("../chat/tts.ts", () => ({ announce: vi.fn(), sessionVoiceOpts: () => undefined }));

const shown: string[] = [];
class FakeNotification {
  static permission = "granted";
  onclick: (() => void) | null = null;
  constructor(_title: string, opts: { tag?: string }) {
    shown.push(opts.tag || "");
  }
  close() {}
}

const row = (seq: number, kind: string, id: string): FleetNotification => ({
  seq, id: `e${seq}`, kind, target: { type: "session", id }, displayName: id,
  payload: {}, createdAt: "2026-09-24T00:00:00Z", seen: false,
});

// The first payload only initializes the store; delivery happens for rows newer than it.
const deliver = (items: FleetNotification[]) => {
  useNotificationStore.getState().reset();
  useNotificationStore.getState().applyPayload({ items: [], maxSeq: 0, sourceState: "ready" });
  useNotificationStore.getState().applyPayload({ items, maxSeq: items.length, sourceState: "ready" });
};

describe("childIdleNotify", () => {
  beforeEach(() => {
    shown.length = 0;
    vi.mocked(announce).mockClear();
    (window as unknown as { Notification: unknown }).Notification = FakeNotification;
    setSetting("ttsSessionNotify", true);
    useSessionsStore.setState({
      sessions: [
        { name: "child", kind: "claude", alive: true, origin: "session", originSession: "parent" },
        { name: "fork", kind: "claude", alive: true, origin: "handoff", originSession: "parent" },
        { name: "parent", kind: "claude", alive: true, origin: "user" },
      ],
    });
  });

  it("notifies a child's idle by default", () => {
    setSetting("childIdleNotify", true);
    deliver([row(1, "answer-ready", "child")]);
    expect(shown).toEqual(["e1"]);
    expect(announce).toHaveBeenCalledTimes(1);
  });

  it("when off, silences only a spawned child's idle", async () => {
    setSetting("childIdleNotify", false);
    deliver([
      row(1, "answer-ready", "child"),
      row(2, "question", "child"),
      row(3, "answer-ready", "fork"),
      row(4, "answer-ready", "parent"),
      row(5, "answer-ready", "unknown-session"),
    ]);
    await Promise.resolve();
    expect(shown).toEqual(["e2", "e3", "e4", "e5"]);
    expect(announce).toHaveBeenCalledTimes(4);
    // The row is still recorded for the center.
    expect(useNotificationStore.getState().items.map((n) => n.id)).toContain("e1");
  });

  it("uses the session passed in before the list", () => {
    setSetting("childIdleNotify", false);
    expect(childIdleMuted("not-listed", { origin: "session" })).toBe(true);
    expect(childIdleMuted("not-listed")).toBe(false);
  });
});
