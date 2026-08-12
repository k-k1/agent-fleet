import { describe, expect, it } from "vitest";
import { expandThinking, getSettings, normalizeClaudeCustomModels, type Settings } from "./settings.ts";

// 純ロジックだが jsdom プロジェクト（.dom.test.tsx）に置く: settings.ts は API クライアント
// 経由で読み込み時に localStorage を触るため、node 環境では import 自体が落ちる。
//
// 「思考を展開して表示」（設定 > エージェント > 各カード > 動作設定）は kind スコープで、
// 未設定は必ずオフ＝従来どおり畳んで出す。サーバー ui-prefs は別バージョンの Console も
// 書くので、boolean 以外が入っていてもオフに倒れることまで固定する。
const withThinking = (map: unknown): Settings =>
  ({ ...getSettings(), expandThinking: map } as Settings);

describe("expandThinking", () => {
  it("defaults to off for every kind", () => {
    const s = withThinking({});
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, "claude")).toBe(false);
  });

  it("applies per kind, independently", () => {
    const s = withThinking({ opencode: true, codex: false });
    expect(expandThinking(s, "opencode")).toBe(true);
    expect(expandThinking(s, "codex")).toBe(false);
    // 設定を持たない kind（cursor / kiro も思考を出す）は巻き添えにしない。
    expect(expandThinking(s, "cursor")).toBe(false);
  });

  it("falls back to off for a missing kind or a broken stored value", () => {
    const s = withThinking({ opencode: "yes", codex: 1 });
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, undefined)).toBe(false);
    expect(expandThinking(withThinking(undefined), "opencode")).toBe(false);
  });
});

describe("normalizeClaudeCustomModels", () => {
  it("trims ids and drops aliases, duplicates, and broken values", () => {
    expect(normalizeClaudeCustomModels([
      " claude-opus-4-8 ", "CLAUDE-OPUS-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]", "opus", "bad model", 42, "",
    ])).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]"]);
  });

  it("falls back to an empty catalog for a broken stored value", () => {
    expect(normalizeClaudeCustomModels("claude-opus-4-8")).toEqual([]);
  });
});
