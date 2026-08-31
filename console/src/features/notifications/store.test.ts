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

// 「未回答のまま停止しました」（docs/log/75）。畳むこと自体は無害（持ち越してあるので
// 失われない）が、利用者はそれを知らない — だから通知は「まだ保留のままだ」と
// 「どこで答えられるか」を言う必要がある。種類ごとに文言が変わることも固定する。
describe("carried-interaction の通知文言", () => {
  beforeEach(() => setLocale("ja"));

  it("種類（質問 / 計画 / 許可）で文言が変わる", () => {
    const q = { ...event("e7", "調査"), kind: "carried-interaction", payload: { interaction: "question" } };
    const p = { ...event("e8", "調査"), kind: "carried-interaction", payload: { interaction: "plan" } };
    const perm = { ...event("e9", "調査"), kind: "carried-interaction", payload: { interaction: "permission" } };

    expect(notificationWording(q).title).toBe("質問が未回答のまま停止しました");
    expect(notificationWording(p).title).toBe("計画の承認が未回答のまま停止しました");
    expect(notificationWording(perm).title).toBe("許可の確認が未回答のまま停止しました");
    // 本文は「どこで答えるか」を必ず含む（通知だけ見た人が次の一手を持てるように）。
    expect(notificationWording(q).body).toContain("ミラー");
    expect(notificationWording(q).body).toContain("調査");
  });

  it("payload が空でも質問として読める（既定に落ちて無言にならない）", () => {
    const bare = { ...event("e10", "調査"), kind: "carried-interaction" };
    expect(notificationWording(bare).title).toBe("質問が未回答のまま停止しました");
  });

  it("英語ロケールでも同じ構造", () => {
    setLocale("en");
    const q = { ...event("e11", "probe"), kind: "carried-interaction", payload: { interaction: "question" } };
    expect(notificationWording(q).title).toBe("Stopped with an unanswered question");
  });
});
