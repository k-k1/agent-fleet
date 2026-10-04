// The notification table's two keys against ui-prefs sync. The Agent replaces the blob wholesale
// on every PUT and an older Console's hydrate merges only the keys it knows, so a tab still
// running the previous Console drops notifyUnread from the server the next time it saves
// anything. That must never change what a device does: this device keeps its own choice, and a
// fresh device reads the legacy switches exactly as before the table existed.
import { beforeEach, afterEach, describe, expect, it, vi } from "vitest";
import { notifyCell, notifyCellPatch } from "../features/notifications/prefs.ts";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";

async function freshSettings(local: Record<string, unknown> = {}) {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  vi.resetModules();
  return await import("./settings.ts");
}

const putBodies = () =>
  apiJSONMock.mock.calls.filter((c) => c[0] === "api/env/ui-prefs" && c[1] === "PUT").map((c) => c[2] as Record<string, unknown>);

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("notification table keys and ui-prefs sync", () => {
  it("keeps this device's choice when an older tab dropped notifyUnread from the server", async () => {
    const s = await freshSettings({ notifyUnread: { "turn-own": false }, notifyDevice: { "schedule.os": false } });
    // What an older Console PUTs: every key it knows, none of the table's.
    apiMock.mockResolvedValueOnce({ childIdleNotify: true, chatSize: 16 });
    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.getSettings().notifyUnread).toEqual({ "turn-own": false });
    expect(notifyCell(s.getSettings(), "turn-own", "unread")).toBe(false);
    expect(notifyCell(s.getSettings(), "schedule", "os")).toBe(false);
  });

  it("gives a fresh device today's behaviour from the legacy switches when the key is gone", async () => {
    const s = await freshSettings({});
    apiMock.mockResolvedValueOnce({ childIdleNotify: false });
    expect(await s.hydrateUIPrefs()).toBe(true);
    const st = s.getSettings();
    expect(st.notifyUnread).toEqual({});
    expect(["unread", "os", "voice"].map((e) => notifyCell(st, "turn-child", e as "os"))).toEqual([false, false, false]);
    expect(["unread", "os", "voice"].map((e) => notifyCell(st, "turn-own", e as "os"))).toEqual([true, true, true]);
    expect(notifyCell(st, "usage-reset", "os")).toBe(true); // usageResetNotify defaults on
  });

  it("syncs notifyUnread written by this Console to another device", async () => {
    const s = await freshSettings({});
    apiMock.mockResolvedValueOnce({ notifyUnread: { schedule: false } });
    await s.hydrateUIPrefs();
    expect(notifyCell(s.getSettings(), "schedule", "unread")).toBe(false);
  });

  it("never sends notifyDevice to the server, nor takes it from there", async () => {
    const s = await freshSettings({});
    apiMock.mockResolvedValueOnce({ notifyDevice: { "turn-own.os": false } });
    await s.hydrateUIPrefs();
    expect(s.isDeviceLocalSetting("notifyDevice")).toBe(true);
    expect(s.isDeviceLocalSetting("notifyUnread")).toBe(false);
    expect(notifyCell(s.getSettings(), "turn-own", "os")).toBe(true);

    s.setSettings(notifyCellPatch(s.getSettings(), "session-report", "os", false));
    s.setSettings(notifyCellPatch(s.getSettings(), "session-report", "unread", false));
    await vi.advanceTimersByTimeAsync(1_000);
    const bodies = putBodies();
    expect(bodies.length).toBeGreaterThan(0);
    for (const b of bodies) expect(b).not.toHaveProperty("notifyDevice");
    expect(bodies.at(-1)!.notifyUnread).toEqual({ "session-report": false });
    // It is kept on this device.
    expect(JSON.parse(localStorage.getItem(SETTINGS_KEY) || "{}").notifyDevice).toEqual({ "session-report.os": false });
  });
});
