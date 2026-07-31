import { beforeEach, describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import type { FleetNotification } from "./store.ts";
import { unseenSessionEventIDs } from "./read.ts";
import { notificationWording } from "./wording.ts";

const event = (id: string, targetID: string, seen = false, type = "session"): FleetNotification => ({
  seq: Number(id.replace(/\D/g, "")) || 1,
  id,
  kind: "answer-ready",
  target: { type, id: targetID },
  displayName: targetID,
  payload: {},
  createdAt: "2026-07-13T00:00:00Z",
  seen,
});

describe("unseenSessionEventIDs", () => {
  it("returns only unread events belonging to the active session", () => {
    const items = [
      event("e1", "active"),
      event("e2", "active", true),
      event("e3", "other"),
      event("e4", "active", false, "usage"),
    ];
    expect(unseenSessionEventIDs(items, "active")).toEqual(["e1"]);
    expect(unseenSessionEventIDs(items, "")).toEqual([]);
  });
});

describe("rate-limit notification wording", () => {
  beforeEach(() => setLocale("ja"));

  it("distinguishes reaching the limit from a confirmed automatic resume", () => {
    const reached = { ...event("e5", "API 整理"), kind: "rate-limit-reached" };
    const resumed = { ...event("e6", "API 整理"), kind: "rate-limit-resumed" };

    expect(notificationWording(reached)).toEqual({
      title: "Claude の利用上限に達しました",
      body: "API 整理 のターンが利用上限で中断しました。",
      speech: "API 整理 のターンが Claude の利用上限で中断しました。",
    });
    expect(notificationWording(resumed).title).toBe("利用上限リセット後に再開しました");

    setLocale("en");
    expect(notificationWording(reached).title).toBe("Claude usage limit reached");
    expect(notificationWording(resumed).body).toBe("An automatic resume was sent to API 整理.");
  });
});
