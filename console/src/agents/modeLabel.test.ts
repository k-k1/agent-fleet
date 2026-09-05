import { describe, expect, it } from "vitest";
import { agentOf, nonPlanModeLabel } from "./registry.ts";

// Mode label on the non-plan side (docs/log/76). Claude's default label "Bypass" names the state
// of a session launched with permission prompts skipped, so showing it for a session with
// approvals on puts "permission prompts: ask every time" next to "start mode: Bypass" in the
// launch dialog.
describe("nonPlanModeLabel", () => {
  it("keeps the bypass label when approvals are skipped (the default)", () => {
    expect(nonPlanModeLabel("claude", true)).toBe("Bypass");
  });

  // With approvals on, claude starts in its own default mode, manual (measured on 2.1.241). The
  // mirror's chip reads the terminal and also prints "Manual", so keep the wording aligned.
  it("names the mode approvals-on sessions actually start in", () => {
    expect(nonPlanModeLabel("claude", false)).toBe("Manual");
  });

  it("leaves other kinds' labels alone — they don't name a permission state", () => {
    for (const kind of ["codex", "cursor", "copilot", "kiro", "opencode"]) {
      expect(nonPlanModeLabel(kind, false)).toBe(agentOf(kind).defaultModeLabel);
      expect(nonPlanModeLabel(kind, true)).toBe(agentOf(kind).defaultModeLabel);
    }
  });
});
