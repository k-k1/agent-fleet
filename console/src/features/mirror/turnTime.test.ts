import { describe, it, expect } from "vitest";
import { carryEnd, endOf, footTime } from "./turnTime.ts";

describe("endOf", () => {
  it("takes the agent-reported end when the row IS a whole turn (opencode/copilot)", () => {
    expect(endOf({ ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:07:00Z" })).toBe("2026-08-04T10:07:00Z");
  });
  it("falls back to the row's own ts when the turn spans many rows (claude/codex)", () => {
    expect(endOf({ ts: "2026-08-04T10:00:00Z" })).toBe("2026-08-04T10:00:00Z");
  });
  it("is empty when the agent records no time at all (cursor/kiro/agy)", () => {
    expect(endOf({})).toBe("");
  });
});

describe("carryEnd", () => {
  it("advances the block's end to the last folded row, keeping the start", () => {
    // claude の1ターン: thinking → ツール呼び出し → 最終テキスト。この不具合の本体で、
    // フッターに出ていたのは 10:00（最初の行）だった。
    const block = { ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:00:00Z" };
    carryEnd(block, { ts: "2026-08-04T10:03:00Z" });
    carryEnd(block, { ts: "2026-08-04T10:07:30Z" });
    expect(block.endTs).toBe("2026-08-04T10:07:30Z");
    expect(block.ts).toBe("2026-08-04T10:00:00Z"); // 並び順の鍵は先頭のまま
  });
  it("keeps the previous end when the folded row carries no time", () => {
    const block = { ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:03:00Z" };
    carryEnd(block, {});
    expect(block.endTs).toBe("2026-08-04T10:03:00Z");
  });
  it("gives a timeless block an end as soon as one folded row has a time", () => {
    const block: { ts?: string; endTs?: string } = {};
    carryEnd(block, { ts: "2026-08-04T10:03:00Z" });
    expect(footTime(block)).toBe("2026-08-04T10:03:00Z");
  });
});

describe("footTime", () => {
  it("shows the end, not the start", () => {
    expect(footTime({ ts: "2026-08-04T10:00:00Z", endTs: "2026-08-04T10:07:00Z" })).toBe("2026-08-04T10:07:00Z");
  });
  it("falls back to the start while a turn is still running (no end yet)", () => {
    expect(footTime({ ts: "2026-08-04T10:00:00Z" })).toBe("2026-08-04T10:00:00Z");
  });
  it("renders nothing when the agent records no time", () => {
    expect(footTime({})).toBe("");
  });
});
