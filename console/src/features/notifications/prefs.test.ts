import { describe, expect, it } from "vitest";
import { NOTIFICATION_KIND_LABELS } from "./wording.ts";
import { NOTIFY_ROWS, notifyCell, notifyCellPatch, notifyRowOf } from "./prefs.ts";

const base = { notifyUnread: {}, notifyDevice: {}, childIdleNotify: true, usageResetNotify: true };

describe("notifyRowOf", () => {
  it("splits a turn finish by whether the session is a child", () => {
    expect(notifyRowOf("answer-ready", false)).toBe("turn-own");
    expect(notifyRowOf("answer-ready", true)).toBe("turn-child");
    expect(notifyRowOf("question", true)).toBe("needs-input");
  });

  it("gives a budget stop its own row, whose dot cannot be muted", () => {
    expect(notifyRowOf("spend-budget", false)).toBe("spend-budget");
    const s = { ...base, notifyUnread: { "spend-budget": false } };
    expect(notifyCell(s, "spend-budget", "unread")).toBe(true);
    expect(notifyCellPatch(s, "spend-budget", "unread", false)).toEqual({});
  });

  it("puts an unknown kind in a row whose cells default on", () => {
    expect(notifyRowOf("a-kind-from-a-newer-cp", false)).toBe("other");
  });

  // A kind added to the center without a thought about its row lands in "other" silently; this
  // makes the choice explicit.
  it("places every known kind on purpose", () => {
    const other = Object.keys(NOTIFICATION_KIND_LABELS).filter((k) => notifyRowOf(k, false) === "other").sort();
    expect(other).toEqual(["arch-residue", "chat-auto-paused", "chat-context-overflow", "chat-context-pressure", "start-deadline", "stop-after-turn", "submodule-sync"]);
  });
});

describe("notifyCell", () => {
  it("is on everywhere for an empty table and legacy switches on", () => {
    for (const r of NOTIFY_ROWS) for (const e of ["unread", "os", "voice"] as const) expect(notifyCell(base, r.row, e)).toBe(true);
  });

  it("falls back to the legacy switch of the child and usage rows", () => {
    const s = { ...base, childIdleNotify: false, usageResetNotify: false };
    expect([notifyCell(s, "turn-child", "unread"), notifyCell(s, "turn-child", "os"), notifyCell(s, "turn-child", "voice")]).toEqual([false, false, false]);
    expect([notifyCell(s, "usage-reset", "os"), notifyCell(s, "usage-reset", "voice")]).toEqual([false, false]);
    expect(notifyCell(s, "usage-reset", "unread")).toBe(true); // usageResetNotify never touched the dot
    expect(notifyCell(s, "turn-own", "os")).toBe(true);
  });

  it("ignores a value of the wrong shape instead of breaking", () => {
    const s = { ...base, notifyUnread: { "turn-own": "no" }, notifyDevice: [] } as never;
    expect(notifyCell(s, "turn-own", "unread")).toBe(true);
    expect(notifyCell(s, "turn-own", "os")).toBe(true);
  });

  it("cannot mute the dot of a row that waits for a person", () => {
    const s = { ...base, notifyUnread: { "needs-input": false, handoff: false, "cloud-login": false } };
    for (const row of ["needs-input", "handoff", "cloud-login"] as const) expect(notifyCell(s, row, "unread")).toBe(true);
    expect(notifyCellPatch(s, "needs-input", "unread", false)).toEqual({});
  });
});

describe("notifyCellPatch", () => {
  const apply = (s: typeof base, p: object) => ({ ...s, ...p }) as typeof base;

  it("stores only a muted dot, so the table stays sparse", () => {
    const off = apply(base, notifyCellPatch(base, "schedule", "unread", false));
    expect(off.notifyUnread).toEqual({ schedule: false });
    expect(apply(off, notifyCellPatch(off, "schedule", "unread", true)).notifyUnread).toEqual({});
  });

  it("writes the child dot into childIdleNotify and pins its other cells first", () => {
    const p = notifyCellPatch(base, "turn-child", "unread", false);
    expect(p).toEqual({ childIdleNotify: false, notifyDevice: { "turn-child.os": true, "turn-child.voice": true } });
    const s = apply(base, p);
    expect([notifyCell(s, "turn-child", "unread"), notifyCell(s, "turn-child", "os"), notifyCell(s, "turn-child", "voice")]).toEqual([false, true, true]);
  });

  it("keeps usageResetNotify meaning 'either usage cell is on' for an older tab", () => {
    const s1 = apply(base, notifyCellPatch(base, "usage-reset", "voice", false));
    expect(s1.usageResetNotify).toBe(true);
    expect([notifyCell(s1, "usage-reset", "os"), notifyCell(s1, "usage-reset", "voice")]).toEqual([true, false]);
    const s2 = apply(s1, notifyCellPatch(s1, "usage-reset", "os", false));
    expect(s2.usageResetNotify).toBe(false);
    // Turning one back on does not drag the other with it through the legacy fallback.
    const s3 = apply(s2, notifyCellPatch(s2, "usage-reset", "os", true));
    expect([notifyCell(s3, "usage-reset", "os"), notifyCell(s3, "usage-reset", "voice"), s3.usageResetNotify]).toEqual([true, false, true]);
  });

  it("does not mutate the maps it was given", () => {
    const s = { ...base, notifyDevice: { "turn-own.os": true } };
    notifyCellPatch(s, "turn-own", "os", false);
    expect(s.notifyDevice).toEqual({ "turn-own.os": true });
  });
});
