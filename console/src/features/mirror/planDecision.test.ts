import { describe, it, expect } from "vitest";
import { PLAN_APPROVE_KEYS, isApproved, isRejected, outcomeHead, planOutcome } from "./planDecision.ts";

// A real approval tool_result from the current CLI (captured from a live transcript):
// header, saved-plan path, then the WHOLE plan Markdown under "## Approved Plan:".
const approvalWith = (plan: string) =>
  "User has approved your plan. You can now start coding. Start with updating your todo list if applicable\n\n" +
  "Your plan has been saved to: /var/lib/af/claude/plans/immutable-dazzling-babbage.md\n" +
  "You can refer back to it if needed during implementation.\n\n" +
  "## Approved Plan:\n" +
  plan;

// Regression guard: clicking reject on a plan actually APPROVED it, while the card stayed badged
// Reject (optimistic). Two separate defects, both covered here:
//   1. reject drove claude's ExitPlanMode menu by a fixed keystroke offset (Down×3),
//      which wraps onto a "Yes" row on shorter CLI menus. Reject is now interrupt-based;
//      approve is the layout-independent "Enter = accept the highlighted default".
//   2. the badge never reconciled the optimistic reject mark against the real outcome.

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

  // Regression guard: the CLI embeds the approved plan in the tool_result, so a keyword match
  // over the whole text reads the PLAN's prose instead of the verdict.
  it("ignores the approved plan the CLI embeds in the result", () => {
    const plan =
      "# フロービルダー UI の移植\n\n## 非目標\n- 既存 API の作り直しはしない（前回の案は却下）。\n" +
      "- 途中で中止せず、モックのまま出す案は refine せず reject する。\n";
    expect(outcomeHead(approvalWith(plan))).not.toContain("却下");
    expect(isApproved(approvalWith(plan))).toBe(true);
    expect(isRejected(approvalWith(plan))).toBe(false);
    // …and the badge that the user actually sees.
    expect(planOutcome(approvalWith(plan), false)).toBe("approved");
    expect(planOutcome(approvalWith(plan), true)).toBe("approved");
  });

  it("still reads a rejection whose feedback mentions approval words", () => {
    // The reject side carries the user's feedback; the verdict is the header, as always.
    expect(planOutcome("[Request interrupted by user for tool use]\nyes, but split it first", false)).toBe("rejected");
  });
});

describe("planOutcome reconciliation", () => {
  it("shows Reject optimistically before the outcome lands", () => {
    // User just clicked reject; the tool_result is a poll or two behind.
    expect(planOutcome(undefined, true)).toBe("rejected");
    expect(planOutcome("", true)).toBe("rejected");
  });

  it("keeps Reject once the real interrupt result lands", () => {
    expect(planOutcome("[Request interrupted by user for tool use]", true)).toBe("rejected");
  });

  it("a definitive approval overrides a stale optimistic Reject (the reported symptom)", () => {
    // The exact screenshot case: optimistic reject flag set, but the transcript says the
    // plan was approved. The badge must correct itself to Approved, not lie as Reject.
    expect(planOutcome("User approved the plan and started coding", true)).toBe("approved");
  });

  it("badges an approval as approved and a rejection as rejected without the optimistic flag", () => {
    expect(planOutcome("proceed", false)).toBe("approved");
    expect(planOutcome("not approved, keep planning", false)).toBe("rejected");
  });

  it("stays neutral (decided) when the outcome is unknown and nothing was optimistically rejected", () => {
    expect(planOutcome("", false)).toBe("decided");
    expect(planOutcome(undefined, false)).toBe("decided");
  });
});
