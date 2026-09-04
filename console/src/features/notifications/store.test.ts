import { beforeEach, describe, expect, it } from "vitest";
import { setLocale } from "../../lib/i18n/index.ts";
import type { FleetNotification } from "./store.ts";
import { unseenSessionEventIDs } from "./read.ts";
import { notificationRowSubtitle, notificationWording } from "./wording.ts";

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

describe("notification row subtitle", () => {
  const report = (payload: Record<string, unknown>) => ({
    kind: "session-report",
    displayName: "プラン検証",
    payload,
  });

  it("a session report shows reporter -> destination conversation", () => {
    expect(notificationRowSubtitle(report({ conversationTitle: "運用オペレーター" }))).toBe("プラン検証 → 運用オペレーター");
  });

  it("falls back to just the reporter when there is no conversation name", () => {
    expect(notificationRowSubtitle(report({}))).toBe("プラン検証");
    expect(notificationRowSubtitle(report({ conversationTitle: "" }))).toBe("プラン検証");
  });

  it("other kinds keep displayName (the conversation name is already in it)", () => {
    expect(notificationRowSubtitle({ kind: "chat-auto-paused", displayName: "運用", payload: { conversationTitle: "運用" } })).toBe("運用");
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

// "Stopped with an unanswered ..." (docs/log/75). Folding a session away is harmless in itself
// (the interaction is carried over, not lost), but the user does not know that, so the
// notification has to say it is still pending and where it can be answered. The wording differing
// per interaction kind is pinned here too.
describe("carried-interaction notification wording", () => {
  beforeEach(() => setLocale("ja"));

  it("the wording differs per kind (question / plan / permission)", () => {
    const q = { ...event("e7", "調査"), kind: "carried-interaction", payload: { interaction: "question" } };
    const p = { ...event("e8", "調査"), kind: "carried-interaction", payload: { interaction: "plan" } };
    const perm = { ...event("e9", "調査"), kind: "carried-interaction", payload: { interaction: "permission" } };

    expect(notificationWording(q).title).toBe("質問が未回答のまま停止しました");
    expect(notificationWording(p).title).toBe("計画の承認が未回答のまま停止しました");
    expect(notificationWording(perm).title).toBe("許可の確認が未回答のまま停止しました");
    // The body must always say where to answer, so someone who only sees the notification has a
    // next step.
    expect(notificationWording(q).body).toContain("ミラー");
    expect(notificationWording(q).body).toContain("調査");
  });

  it("an empty payload still reads as a question, instead of silently taking a default", () => {
    const bare = { ...event("e10", "調査"), kind: "carried-interaction" };
    expect(notificationWording(bare).title).toBe("質問が未回答のまま停止しました");
  });

  it("the same structure in the English locale", () => {
    setLocale("en");
    const q = { ...event("e11", "probe"), kind: "carried-interaction", payload: { interaction: "question" } };
    expect(notificationWording(q).title).toBe("Stopped with an unanswered question");
  });
});

describe("scheduled-run notification wording (docs/log/38)", () => {
  beforeEach(() => setLocale("ja"));

  const sched = (kind: string, status: string) => ({
    ...event("e20", "sch_1", false, "schedule"),
    kind,
    displayName: "毎朝レビュー",
    payload: { schedule_id: "sch_1", status, spec_label: "毎朝レビュー", spec: "0 9 * * *" },
  });

  // The point: while schedule-* had no branch, an unknown kind fell through to the usage-reset
  // wording at the end, so the notification arrived but read as a usage-limit reset — from the
  // user's side indistinguishable from the schedule silently not running.
  it("does not fall through to the usage-reset wording", () => {
    const w = notificationWording(sched("schedule-failed", "error:agent not ready"));
    expect(w.title).toBe("定時実行が失敗しました");
    expect(w.title).not.toContain("リセット");
    expect(w.speech).toContain("毎朝レビュー");
  });

  // A failure's body is the cause itself: only status answers "why didn't it run".
  it("a failure passes the reason through into the body", () => {
    const w = notificationWording(sched("schedule-failed", "error:agent not ready: timed out waiting for agent"));
    expect(w.body).toContain("毎朝レビュー");
    expect(w.body).toContain("timed out waiting for agent");
  });

  it("a skip renders a known reason as translated text", () => {
    const w = notificationWording(sched("schedule-skipped", "skipped_quota"));
    expect(w.title).toBe("定時実行が見送られました");
    expect(w.body).toContain("上限");
  });

  it("the same structure in the English locale", () => {
    setLocale("en");
    const w = notificationWording(sched("schedule-failed", "skipped_target_missing"));
    expect(w.title).toBe("A schedule did not run");
    expect(w.body).toContain("no longer exists");
  });
});
