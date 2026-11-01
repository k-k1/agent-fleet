// The board's wording (ADR 0111 decision 6). What is pinned is honesty about age: a value is a
// lower bound from when it was observed, past its reset it is never a percentage, and nothing
// received is "unknown".
import { describe, it, expect, vi } from "vitest";

// usageResetNotify.ts imports the API client, which reads localStorage at load.
vi.mock("../../core/api/client.ts", () => ({ apiJSON: vi.fn() }));
import { t } from "../../lib/i18n/index.ts";
import { agoText, effectiveStatus, unitText, sessionText, unitLabel } from "./limitBoard.ts";
import { usageObservationBody } from "../../app/usageResetNotify.ts";

const now = Date.parse("2026-10-12T12:00:00Z");

describe("limit board wording", () => {
  it("states a lower bound with its age", () => {
    expect(unitText(t, { unit: "5h", status: "current", usedPct: 42.4, band: "lt50", observedAt: "2026-10-12T11:50:00Z" }, now)).toBe(
      "42% 以上（10 分前）",
    );
  });
  it("marks a stale value and keeps it as a lower bound", () => {
    const s = unitText(t, { unit: "7d", status: "stale", band: "lt80", observedAt: "2026-10-12T09:00:00Z" }, now);
    expect(s).toContain("50〜80%");
    expect(s).toContain("3 時間前");
    expect(s).toContain("古い値");
  });
  it("never shows a percentage or a band past the reset", () => {
    const s = unitText(t, { unit: "5h", status: "reset_not_observed", usedPct: 99, band: "ge95" }, now);
    expect(s).toBe("リセット未確認");
    expect(s).not.toMatch(/%/);
  });
  it("says unknown, never available, without a value or a time", () => {
    expect(unitText(t, { unit: "5h", status: "current" }, now)).toBe("不明");
    expect(agoText(t, undefined, now)).toBe("不明");
  });
  it("never words a spend limit with a resume time", () => {
    expect(sessionText(t, { state: "spend_limit", resumeAt: "2026-10-12T13:00:00Z" })).not.toMatch(/\d\d:\d\d/);
    expect(sessionText(t, { state: "limited", resumeAt: "2026-10-12T13:00:00Z" })).toMatch(/\d\d:\d\d/);
  });
  // Review finding (#2017): a board left open must age its entries itself between re-reads.
  it("re-applies the stale and reset boundaries at render time", () => {
    const u = { unit: "5h", status: "current" as const, usedPct: 99, resetsAt: "2026-10-12T13:00:00Z", observedAt: "2026-10-12T11:55:00Z" };
    expect(effectiveStatus(u, now)).toBe("current");
    expect(effectiveStatus({ ...u, resetsAt: "2026-10-13T00:00:00Z" }, now + 2 * 3600_000)).toBe("stale");
    expect(effectiveStatus(u, Date.parse("2026-10-12T13:00:00Z"))).toBe("reset_not_observed");
    expect(unitText(t, u, now + 2 * 3600_000)).toBe("リセット未確認");
    expect(unitText(t, { ...u, resetsAt: undefined, usedPct: undefined, band: "ge95" }, now + 2 * 3600_000)).toContain("古い値");
  });
  it("labels muse windows by their length and copilot pools by name", () => {
    expect(unitLabel(t, "muse", { unit: "window", windowMins: 300, status: "current" })).toBe("5 時間ウィンドウ");
    expect(unitLabel(t, "copilot", { unit: "premium_interactions", status: "current" })).toBe("プレミアムリクエスト");
  });
});

describe("usage observation post", () => {
  const win = (pct: number, extra: object = {}) => ({ pct, resetsAt: "2026-10-12T14:00:00Z", ...extra });
  it("passes the Agent's observation time and epoch through", () => {
    const body = usageObservationBody("claude", {
      fiveHour: win(40),
      observedAt: "2026-10-12T11:50:00Z",
      limitEpoch: "e1",
      limitEpochSince: "2026-10-12T08:00:00Z",
    });
    expect(body).toEqual({
      source: "claude",
      limitEpoch: "e1",
      limitEpochSince: "2026-10-12T08:00:00Z",
      windows: [{ windowKey: "5h", percent: 40, resetsAt: "2026-10-12T14:00:00Z", observedAt: "2026-10-12T11:50:00Z" }],
    });
  });
  it("never makes an observation time up", () => {
    const body = usageObservationBody("codex", { fiveHour: win(40) });
    expect(body?.windows[0]).not.toHaveProperty("observedAt");
    expect(body).not.toHaveProperty("limitEpoch");
  });
  it("posts nothing for a kind the API refuses as a source", () => {
    expect(usageObservationBody("muse", { fiveHour: win(40), sevenDay: win(50) })).toBeNull();
    expect(usageObservationBody("opencode", { fiveHour: win(40) })).toBeNull();
    expect(usageObservationBody("claude", { fiveHour: win(40) })).not.toBeNull();
    expect(usageObservationBody("codex", { sevenDay: win(40) })).not.toBeNull();
  });
  it("drops stale windows and flags derived ones", () => {
    expect(usageObservationBody("codex", { fiveHour: win(0, { stale: true, derived: true }) })).toBeNull();
    const body = usageObservationBody("codex", { sevenDay: win(0, { derived: true }) });
    expect(body?.windows[0]).toMatchObject({ windowKey: "7d", derived: true });
  });
});
