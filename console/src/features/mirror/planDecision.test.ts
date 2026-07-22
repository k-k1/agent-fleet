import { describe, it, expect } from "vitest";
import { PLAN_APPROVE_KEYS, isApproved, isRejected, planOutcome } from "./planDecision.ts";

// Regression guard for the 2026-07-22 bug: clicking 却下 on a plan actually APPROVED it,
// yet the card stayed badged 却下 (optimistic). Two separate defects, both covered here:
//   1. reject drove claude's ExitPlanMode menu by a fixed keystroke offset (Down×3),
//      which wraps onto a "Yes" row on shorter CLI menus. Reject is now interrupt-based;
//      approve is the layout-independent "Enter = accept the highlighted default".
//   2. the badge never reconciled the optimistic 却下 mark against the real outcome.

describe("plan approve/reject strategy", () => {
  it("approves with Enter only — never a positional Down offset", () => {
    // The whole bug was navigating a version-variable menu by position. Approve must
    // select the highlighted default (always a Yes), which is layout-independent.
    expect([...PLAN_APPROVE_KEYS]).toEqual(["Enter"]);
    expect(PLAN_APPROVE_KEYS).not.toContain("Down");
  });
});

describe("isApproved / isRejected", () => {
  it("classifies real ExitPlanMode outcome texts", () => {
    expect(isApproved("User approved the plan")).toBe(true);
    expect(isApproved("承認しました。実行します")).toBe(true);
    // An interrupt is how a reject/Escape is recorded — must NOT read as approval.
    expect(isApproved("[Request interrupted by user for tool use]")).toBe(false);
    expect(isRejected("[Request interrupted by user for tool use]")).toBe(true);
    expect(isRejected("Keep planning")).toBe(true);
    expect(isRejected("却下")).toBe(true);
  });
});

describe("planOutcome reconciliation", () => {
  it("shows 却下 optimistically before the outcome lands", () => {
    // User just clicked reject; the tool_result is a poll or two behind.
    expect(planOutcome(undefined, true)).toBe("rejected");
    expect(planOutcome("", true)).toBe("rejected");
  });

  it("keeps 却下 once the real interrupt result lands", () => {
    expect(planOutcome("[Request interrupted by user for tool use]", true)).toBe("rejected");
  });

  it("a definitive approval overrides a stale optimistic 却下 (the reported symptom)", () => {
    // The exact screenshot case: optimistic reject flag set, but the transcript says the
    // plan was approved. The badge must correct itself to 承認, not lie as 却下.
    expect(planOutcome("User approved the plan and started coding", true)).toBe("approved");
  });

  it("badges an approval as approved and a rejection as rejected without the optimistic flag", () => {
    expect(planOutcome("proceed", false)).toBe("approved");
    expect(planOutcome("not approved, keep planning", false)).toBe("rejected");
  });

  it("stays neutral (決定済み) when the outcome is unknown and nothing was optimistically rejected", () => {
    expect(planOutcome("", false)).toBe("decided");
    expect(planOutcome(undefined, false)).toBe("decided");
  });
});
