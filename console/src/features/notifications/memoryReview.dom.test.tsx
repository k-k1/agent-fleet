// A notification the retired background memory review raised earlier (ADR 0117, removed by ADR 0130
// decision 2) still renders from its stored counts and links to the change list. Nothing raises one now.
import { beforeEach, describe, expect, it, vi } from "vitest";
import { openNotificationTarget, type FleetNotification } from "./store.ts";
import { notificationKindLabel, notificationRowSubtitle, notificationWording } from "./wording.ts";
import { notifyRowOf } from "./prefs.ts";
import { useSettingsUI } from "../settings/store.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { allViews, freshLayout } from "../../layout/ops.ts";
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
    useLayoutStore.setState({ layout: freshLayout() });
  });

  it("words the counts in both languages and puts them on the row", () => {
    setLocale("ja");
    const n = notification();
    const w = notificationWording(n);
    expect(w.title).toBe("メモリの提案が 5 件、未処理のままです");
    expect(w.body).toContain("新しい提案 2 件");
    expect(w.body).toContain("全部で 5 件");
    expect(w.body).not.toContain("提案 で承認");
    expect(notificationRowSubtitle(n)).toBe(w.body);
    expect(notificationKindLabel("memory-review")).toBe(ja["noti.kind_memory_review"]);
    setLocale("en");
    expect(notificationWording(n).title).toBe("5 memory suggestions were left unreviewed");
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

  it("opens the Memory view on Changes when it is clicked, and counts as opened", async () => {
    const r = await openNotificationTarget(notification(), false);
    expect(r.opened).toBe(true);
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    const pane = allViews(useLayoutStore.getState().layout).find((v) => v.content.kind === "memory");
    expect(pane?.content).toEqual({ kind: "memory", memoryView: "changes" });
  });
});

// The consolidator's daily notice (ADR 0130 decision 7) opens the proposals in the Memory view
// (#3720), not in the Settings modal.
describe("a memory-consolidation notification", () => {
  const consolidation = (): FleetNotification => ({ ...notification({ pending: 3 }), id: "memory-consolidation-1", kind: "memory-consolidation" });
  const memoryPanes = () => allViews(useLayoutStore.getState().layout).filter((v) => v.content.kind === "memory");

  beforeEach(() => {
    useSettingsUI.getState().closeSettings();
    useLayoutStore.setState({ layout: freshLayout() });
  });

  it("opens the proposals in a Memory view pane and leaves Settings closed", async () => {
    const r = await openNotificationTarget(consolidation(), false);
    expect(r.opened).toBe(true);
    expect(useSettingsUI.getState().settingsOpen).toBe(false);
    expect(memoryPanes().map((v) => v.content)).toEqual([{ kind: "memory", memoryView: "proposals" }]);
  });

  it("retargets the Memory view that is already open instead of opening a second one", async () => {
    useLayoutStore.getState().openTarget({ content: { kind: "memory", memoryView: "memories" } });
    await openNotificationTarget(consolidation(), false);
    expect(memoryPanes().map((v) => v.content)).toEqual([{ kind: "memory", memoryView: "proposals" }]);
  });
});
