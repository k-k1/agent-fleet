import { describe, it, expect } from "vitest";
import { contextWindow } from "./ContextBar.tsx";

// Go 側の contextWindowGuess()（workspace/agent/session_usage.go）と対の回帰テスト。
// 「claude-opus-5 が 200k と誤認される」の真因は 1M 側を列挙する設計だったこと。
// 既定 1M・200k 側だけ例外、に反転済みなので、未知の将来モデルが 1M に落ちること
// （＝列挙漏れで再発しないこと）まで固定する。新モデル追加時は Go 側と両方を見ること。
describe("contextWindow", () => {
  it("Claude は既定 1M（未知の将来モデルを含む）", () => {
    for (const m of [
      "claude-opus-5",
      "claude-sonnet-5",
      "claude-opus-4-8",
      "claude-opus-4-7",
      "claude-opus-4-6",
      "claude-sonnet-4-6",
      "claude-fable-5",
      "claude-mythos-5",
      "anthropic/claude-sonnet-5", // opencode の provider 付き
      "claude-opus-9", // 未知の将来モデル
    ]) {
      expect(contextWindow(m, 0), m).toBe(1000000);
    }
  });

  it("200k 側の例外（世代番号の 4-5 を 5 と取り違えない）", () => {
    for (const m of [
      "claude-opus-4-5",
      "claude-sonnet-4-5-20250929",
      "claude-opus-4-1",
      "claude-opus-4-20250514", // 日付入りID
      "claude-3-5-sonnet-20241022",
      "claude-3-7-sonnet",
      "claude-haiku-4-5-20251001",
    ]) {
      expect(contextWindow(m, 0), m).toBe(200000);
    }
  });

  it("GPT-5.x は 272k", () => {
    expect(contextWindow("gpt-5.1-codex", 0)).toBe(272000);
  });

  it("素性の分からない非 Claude は 200k、実績が超えたら 1M へ伸ばす", () => {
    expect(contextWindow("some-unknown-model", 0)).toBe(200000);
    expect(contextWindow("some-unknown-model", 250000)).toBe(1000000);
  });
});
