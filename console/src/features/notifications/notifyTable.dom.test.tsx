// The notification table (prefs.ts) took over every per-kind decision deliver() and the
// mark-read-on-arrival sweep used to make from three loose switches. The upgrade promise is that
// nobody's notifications change: with the table's own keys empty, every combination of the legacy
// switches must produce exactly what the code before the table produced. `before` below is that
// code's decision, transcribed; the matrix compares it against the real store for every kind.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Cell, Layout, View } from "../../layout/types.ts";

const apiJSON = vi.fn(async (..._args: unknown[]) => ({}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  chatGet: async () => ({ id: "alive" }),
}));
const announce = vi.fn();
vi.mock("../chat/tts.ts", () => ({ announce: (...a: unknown[]) => announce(...a), sessionVoiceOpts: () => undefined }));

const { useNotificationStore, wireNotificationReadOnVisibleSessions } = await import("./store.ts");
const { useLayoutStore } = await import("../../layout/store.ts");
const { useSessionsStore } = await import("../sessions/store.ts");
const { setSettings } = await import("../../lib/settings.ts");
type FleetNotification = import("./store.ts").FleetNotification;

const shown: string[] = [];
class FakeNotification {
  static permission = "granted";
  onclick: (() => void) | null = null;
  constructor(_title: string, opts: { tag?: string }) {
    shown.push(opts.tag || "");
  }
  close() {}
}

const view = (id: string, session: string): View => ({ id, session, content: { kind: "terminal", chat: false }, wrap: null });
const cell = (id: string, views: View[]): Cell => ({ id, views, selectedViewId: views[0].id });
const layout = (cells: Cell[]): Layout => ({
  version: 3, mode: "tabs", cols: [{ id: "c0", rowRatio: 0.5, cells }], colRatios: [1], activeCellId: cells[0].id,
});

// One notification per row of the table, plus a kind this Console has never heard of.
const SAMPLES: [string, string, string][] = [
  ["answer-ready", "session", "parent"],
  ["answer-ready", "session", "child"],
  ["answer-ready", "session", "fork"],
  ["question", "session", "child"],
  ["permission-request", "session", "parent"],
  ["usage-reset", "workspace", ""],
  ["session-report", "session", "parent"],
  ["rate-limit-reached", "session", "parent"],
  ["schedule-failed", "schedule", "s1"],
  ["handoff-offer", "session", "someone"],
  ["aws-login-required", "workspace", ""],
  ["terminal-notification", "session", "parent"],
  ["chat-auto-paused", "session", "parent"],
  ["a-kind-from-a-newer-cp", "session", "parent"],
];

const rows = (): FleetNotification[] =>
  SAMPLES.map(([kind, type, id], i) => ({
    seq: i + 1, id: `e${i + 1}`, kind, target: { type, id }, displayName: id,
    payload: {}, createdAt: "2026-10-04T00:00:00Z", seen: false,
  }));

interface Legacy {
  childIdleNotify: boolean;
  usageResetNotify: boolean;
  ttsSessionNotify: boolean;
  ttsEnabled: boolean;
}

// The decisions as the code before the table made them (store.ts deliver and mutedChildIdleIDs).
function before(n: FleetNotification, s: Legacy): { os: boolean; voice: boolean; markedRead: boolean } {
  const childIdle = n.kind === "answer-ready" && n.target.type === "session" && n.target.id === "child";
  if (childIdle && !s.childIdleNotify) return { os: false, voice: false, markedRead: true };
  const os = n.kind !== "usage-reset" || s.usageResetNotify;
  const voice = n.kind === "usage-reset" ? s.usageResetNotify && s.ttsEnabled : s.ttsSessionNotify;
  return { os, voice, markedRead: false };
}

const acked = (): string[] =>
  apiJSON.mock.calls
    .filter((c) => c[0] === "api/notifications/seen")
    .flatMap((c) => ((c[2] as { eventIds?: string[] })?.eventIds ?? []));

// The first payload only initializes the store; delivery happens for rows newer than it.
async function arrive(items: FleetNotification[]) {
  useNotificationStore.getState().reset();
  useNotificationStore.getState().applyPayload({ items: [], maxSeq: 0, sourceState: "ready" });
  useNotificationStore.getState().applyPayload({ items, maxSeq: items.length, sourceState: "ready" });
  await Promise.resolve();
}

let stop: (() => void) | null = null;

beforeEach(() => {
  shown.length = 0;
  announce.mockClear();
  apiJSON.mockClear();
  (window as unknown as { Notification: unknown }).Notification = FakeNotification;
  useSessionsStore.setState({
    sessions: [
      { name: "child", kind: "claude", alive: true, origin: "session", originSession: "parent" },
      { name: "fork", kind: "claude", alive: true, origin: "handoff", originSession: "parent" },
      { name: "parent", kind: "claude", alive: true, origin: "user" },
    ],
  });
  // The pane shows a session none of the samples target, so nothing is suppressed as "on screen".
  useLayoutStore.setState({ layout: layout([cell("g1", [view("p1", "elsewhere")])]) });
  setSettings({ notifyUnread: {}, notifyDevice: {}, childIdleNotify: true, usageResetNotify: true, ttsSessionNotify: true, ttsEnabled: true });
});

afterEach(() => {
  stop?.();
  stop = null;
});

describe("upgrade: an empty table behaves exactly as the legacy switches did", () => {
  const combos: Legacy[] = [];
  for (let i = 0; i < 16; i++) {
    combos.push({ childIdleNotify: !!(i & 1), usageResetNotify: !!(i & 2), ttsSessionNotify: !!(i & 4), ttsEnabled: !!(i & 8) });
  }
  it.each(combos)("childIdle=$childIdleNotify usageReset=$usageResetNotify sessionVoice=$ttsSessionNotify tts=$ttsEnabled", async (legacy) => {
    setSettings({ ...legacy, notifyUnread: {}, notifyDevice: {} });
    const items = rows();
    stop = wireNotificationReadOnVisibleSessions();
    await arrive(items);
    for (const n of items) {
      const want = before(n, legacy);
      expect({ kind: n.kind, target: n.target.id, os: shown.includes(n.id) }).toEqual({ kind: n.kind, target: n.target.id, os: want.os });
      expect({ kind: n.kind, target: n.target.id, read: acked().includes(n.id) }).toEqual({ kind: n.kind, target: n.target.id, read: want.markedRead });
    }
    // announce does not carry the event id; its fourth argument is the speaking session (empty
    // for a notification aimed at no session of ours), so compare those as a multiset.
    const spoke = announce.mock.calls.map((c) => String(c[3])).sort();
    const want = items.filter((n) => before(n, legacy).voice).map((n) => (n.target.type === "session" ? n.target.id : "")).sort();
    expect(spoke).toEqual(want);
  });
});

describe("the table's new cells", () => {
  it("turns one row's OS notification off and leaves its voice and every other row alone", async () => {
    setSettings({ notifyDevice: { "session-report.os": false } });
    const items = rows();
    await arrive(items);
    const report = items.find((n) => n.kind === "session-report")!;
    expect(shown).not.toContain(report.id);
    expect(shown).toHaveLength(items.length - 1);
    expect(announce).toHaveBeenCalledTimes(items.length);
  });

  it("turns one row's read-aloud off", async () => {
    setSettings({ notifyDevice: { "needs-input.voice": false } });
    const items = rows();
    await arrive(items);
    const asks = items.filter((n) => n.kind === "question" || n.kind === "permission-request");
    expect(shown).toHaveLength(items.length);
    expect(announce).toHaveBeenCalledTimes(items.length - asks.length);
  });

  it("marks a muted row read on arrival, but never a row that waits for a person", async () => {
    // needs-input cannot be muted; a stored false (an import, a hand edit) is ignored.
    setSettings({ notifyUnread: { "turn-own": false, "needs-input": false } });
    const items = rows();
    stop = wireNotificationReadOnVisibleSessions();
    await arrive(items);
    const ownTurns = items.filter((n) => n.kind === "answer-ready" && n.target.id !== "child").map((n) => n.id);
    expect(acked().sort()).toEqual(ownTurns.sort());
  });

  it("lets a child's OS notification stay on with its dot muted", async () => {
    setSettings({ childIdleNotify: false, notifyDevice: { "turn-child.os": true } });
    const items = rows();
    stop = wireNotificationReadOnVisibleSessions();
    await arrive(items);
    const childIdle = items.find((n) => n.kind === "answer-ready" && n.target.id === "child")!;
    expect(shown).toContain(childIdle.id);
    expect(acked()).toEqual([childIdle.id]);
    // Its voice still falls back to childIdleNotify (off).
    expect(announce).toHaveBeenCalledTimes(items.length - 1);
  });
});
