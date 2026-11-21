// The four auto-run notification kinds (ADR 0137, target type memo-queue) and a kind the Console does
// not know (#4217): their wording in both languages, their heading, their preference row and where a
// click goes. An unknown kind must never be worded as another kind's notice.
import { afterEach, describe, expect, it } from "vitest";
import { notificationKindLabel, notificationWording, notificationRowSubtitle } from "./wording.ts";
import { notifyCell, notifyRowOf } from "./prefs.ts";
import { openNotificationTarget, type FleetNotification } from "./store.ts";
import { useMemoStore } from "../memo/store.ts";
import { setLocale, t } from "../../lib/i18n/index.ts";

const KINDS = ["memo-delivery-overdue", "memo-queue-paused", "memo-queue-waiting", "memo-delivery-unconfirmed"] as const;
const note = (kind: string, over: Partial<FleetNotification> = {}): FleetNotification => ({
  seq: 1, id: "e1", kind, target: { type: "memo-queue", id: "c1" }, displayName: "/triage",
  payload: { queue_id: "c1", label: "/triage" }, createdAt: "2026-11-15T00:00:00Z", seen: false, ...over,
});

afterEach(() => setLocale("ja"));

describe.each(["en", "ja"] as const)("the auto-run kinds in %s", (locale) => {
  it.each(KINDS)("%s has its own title, body, speech and heading", (kind) => {
    setLocale(locale);
    const w = notificationWording(note(kind));
    const usage = notificationWording({ kind: "usage-reset", displayName: "x", payload: {} });
    expect(w.title).not.toBe(usage.title);
    expect(w.title).not.toBe(t("notif.unknown.title"));
    // The category, without the empty repository's leading slash.
    expect(w.body).toContain("triage");
    expect(w.body).not.toContain("/triage");
    expect(w.speech).toContain("triage");
    const label = notificationKindLabel(kind);
    expect(label).not.toBe(kind);
    expect(notificationRowSubtitle(note(kind))).toBe("triage");
    // Members do not see the engine's words.
    for (const text of [w.title, w.body, w.speech, label]) expect(text).not.toMatch(/queue|slot|delivery|retire|キュー|スロット/i);
  });

  it("each kind reads differently from the others", () => {
    setLocale(locale);
    const titles = KINDS.map((k) => notificationWording(note(k)).title);
    expect(new Set(titles).size).toBe(4);
  });
});

describe("a kind the Console does not know (#4217)", () => {
  it("is worded neutrally with its identifier and name, never as a usage reset", () => {
    setLocale("en");
    const w = notificationWording({ kind: "a-kind-from-a-newer-cp", displayName: "Some name", payload: { windowKey: "5h", source: "claude" } });
    const usage = notificationWording({ kind: "usage-reset", displayName: "claude", payload: { windowKey: "5h", source: "claude" } });
    expect(w.title).toBe(t("notif.unknown.title"));
    expect(w.body).toBe("a-kind-from-a-newer-cp — Some name");
    expect(w.speech).toBe(t("notif.unknown.speech"));
    for (const k of ["title", "body", "speech"] as const) expect(w[k]).not.toBe(usage[k]);
    expect(w.title + w.body + w.speech).not.toMatch(/limit|reset/i);
    setLocale("ja");
    expect(notificationWording({ kind: "a-kind-from-a-newer-cp", displayName: "", payload: {} }).body).toBe("a-kind-from-a-newer-cp");
    expect(notificationKindLabel("a-kind-from-a-newer-cp")).toBe("a-kind-from-a-newer-cp");
  });

  it("usage-reset keeps its own wording", () => {
    setLocale("en");
    const w = notificationWording({ kind: "usage-reset", displayName: "claude", payload: { windowKey: "5h", source: "claude" } });
    expect(w.title).toBe(t("notif.usage_reset.title", { source: "Claude" }));
    expect(w.body).toBe(t("notif.usage_reset.body", { window: t("notif.window.5h") }));
  });

  it("a click on it opens nothing and does not borrow a destination", async () => {
    const r = await openNotificationTarget(note("a-kind-from-a-newer-cp"), false);
    expect(r.opened).toBe(false);
    expect(useMemoStore.getState().reveal.id).toBeNull();
  });
});

describe("preferences", () => {
  it("puts the four kinds in one auto-run row whose dot cannot be muted", () => {
    for (const k of KINDS) expect(notifyRowOf(k, false)).toBe("auto-run");
    const muted = { notifyUnread: { "auto-run": false }, notifyDevice: {}, childIdleNotify: true, usageResetNotify: true };
    expect(notifyCell(muted, "auto-run", "unread")).toBe(true);
    expect(notifyCell(muted, "auto-run", "os")).toBe(true);
    expect(notifyCell({ ...muted, notifyDevice: { "auto-run.os": false } }, "auto-run", "os")).toBe(false);
  });
});

describe("clicking an auto-run notification opens the category", () => {
  it.each([
    ["memo-queue-paused", false],
    ["memo-queue-waiting", true],
    ["memo-delivery-overdue", true],
    ["memo-delivery-unconfirmed", true],
  ] as const)("%s reveals the category (history: %s)", async (kind, history) => {
    const before = useMemoStore.getState().reveal.n;
    const r = await openNotificationTarget(note(kind), false);
    expect(r.opened).toBe(true);
    expect(useMemoStore.getState().reveal).toEqual({ id: "c1", history, slotsFull: false, n: before + 1 });
  });
});
