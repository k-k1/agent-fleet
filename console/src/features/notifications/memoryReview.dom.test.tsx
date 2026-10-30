// The background memory review's notification (ADR 0117 decision 6): counts only, worded in both
// languages, and a click that leads to the one place the proposals are decided.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { openNotificationTarget, type FleetNotification } from "./store.ts";
import { notificationKindLabel, notificationRowSubtitle, notificationWording } from "./wording.ts";
import { notifyRowOf } from "./prefs.ts";
import { useSettingsUI } from "../settings/store.ts";
import { setLocale } from "../../lib/i18n/index.ts";
import { ja } from "../../lib/i18n/locales/ja.ts";
import { en } from "../../lib/i18n/locales/en.ts";

vi.mock("../../ui/toast.ts", () => ({ toast: vi.fn() }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  apiJSON: vi.fn(),
  api: vi.fn(async () => ({})),
}));

const notification = (payload: Record<string, unknown> = { queued: 2, pending: 5 }): FleetNotification => ({
  seq: 1,
  id: "memory-review-1",
  kind: "memory-review",
  target: { type: "workspace", id: "" },
  displayName: "",
  payload,
  createdAt: "2026-10-19T00:00:00Z",
  seen: false,
});

describe("a memory-review notification", () => {
  beforeEach(() => {
    useSettingsUI.getState().closeSettings();
  });

  it("words the counts in both languages and puts them on the row", () => {
    setLocale("ja");
    const n = notification();
    const w = notificationWording(n);
    expect(w.title).toBe("メモリの提案が 5 件、承認待ちです");
    expect(w.body).toContain("新しい提案 2 件");
    expect(w.body).toContain("全部で 5 件");
    expect(notificationRowSubtitle(n)).toBe(w.body);
    expect(notificationKindLabel("memory-review")).toBe(ja["noti.kind_memory_review"]);
    setLocale("en");
    expect(notificationWording(n).title).toBe("5 memory suggestions are waiting for your approval");
    expect(notificationKindLabel("memory-review")).toBe(en["noti.kind_memory_review"]);
    setLocale("ja");
  });

  it("reads a payload without counts as zero rather than printing undefined", () => {
    setLocale("ja");
    const w = notificationWording(notification({}));
    expect(w.title).not.toMatch(/undefined|NaN/);
    expect(w.body).not.toMatch(/undefined|NaN/);
  });

  it("belongs to the 'other' row of the settings table, which is on by default", () => {
    expect(notifyRowOf("memory-review", false)).toBe("other");
  });

  it("opens Settings > Agent memory > Changes when it is clicked, and counts as opened", async () => {
    const r = await openNotificationTarget(notification(), false);
    expect(r.opened).toBe(true);
    const ui = useSettingsUI.getState();
    expect(ui.settingsOpen).toBe(true);
    expect(ui.settingsSection).toBe("memory");
    expect(ui.settingsView).toBe("changes");
  });
});
