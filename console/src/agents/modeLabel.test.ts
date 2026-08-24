import { describe, expect, it } from "vitest";
import { agentOf, nonPlanModeLabel } from "./registry.ts";

// 非 plan 側のモード表示名（docs/76）。claude の既定ラベル "Bypass" は「権限確認を
// スキップして起動したときの状態名」なので、承認ありのセッションでそのまま出すと
// 起動ダイアログの中で「権限確認: 毎回たずねる」と「開始モード: Bypass」が並ぶ。
describe("nonPlanModeLabel", () => {
  it("keeps the bypass label when approvals are skipped (the default)", () => {
    expect(nonPlanModeLabel("claude", true)).toBe("Bypass");
  });

  it("drops the bypass label when the session asks for approvals", () => {
    expect(nonPlanModeLabel("claude", false)).toBe("Default");
  });

  it("leaves other kinds' labels alone — they don't name a permission state", () => {
    for (const kind of ["codex", "cursor", "copilot", "kiro", "opencode"]) {
      expect(nonPlanModeLabel(kind, false)).toBe(agentOf(kind).defaultModeLabel);
      expect(nonPlanModeLabel(kind, true)).toBe(agentOf(kind).defaultModeLabel);
    }
  });
});
