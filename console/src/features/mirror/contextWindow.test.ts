import { describe, it, expect } from "vitest";
import { contextWindow } from "./ContextBar.tsx";

// Go 側の contextWindowGuess()（workspace/agent/session_usage.go）と対の回帰テスト。
// 「claude-opus-5 が 200k と誤認される」= 現行 1M ネイティブ世代のパターン漏れが真因なので、
// 新モデル追加時にここと Go 側の両方を必ず更新すること。
describe("contextWindow", () => {
  it("現行 1M ネイティブ世代は 1M", () => {
    for (const m of [
      "claude-opus-5",
      "claude-sonnet-5",
      "claude-opus-4-8",
      "claude-opus-4-7",
      "claude-opus-4-6",
      "claude-sonnet-4-6",
      "claude-fable-5",
      "claude-mythos-5",
    ]) {
      expect(contextWindow(m, 0), m).toBe(1000000);
    }
  });

  it("世代番号の 4-5 を 5 と取り違えない（旧世代は 200k）", () => {
    for (const m of ["claude-opus-4-5", "claude-sonnet-4-5-20250929", "claude-3-5-sonnet-20241022"]) {
      expect(contextWindow(m, 0), m).toBe(200000);
    }
  });

  it("haiku は 200k、GPT-5.x は 272k", () => {
    expect(contextWindow("claude-haiku-4-5-20251001", 0)).toBe(200000);
    expect(contextWindow("gpt-5.1-codex", 0)).toBe(272000);
  });

  it("未知モデルは 200k、ただし実績が超えたら 1M へ伸ばす", () => {
    expect(contextWindow("some-unknown-model", 0)).toBe(200000);
    expect(contextWindow("some-unknown-model", 250000)).toBe(1000000);
  });
});
