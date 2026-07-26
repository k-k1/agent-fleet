import { describe, expect, it } from "vitest";
import { patchAnswers } from "./interactionAnswers.ts";

describe("patchAnswers", () => {
  it("replaces a stale plan result with the authoritative answer for its tool-use id", () => {
    const turns = [{
      role: "assistant",
      text: "",
      parts: [{
        kind: "plan",
        qid: "plan-approved",
        plan: "# Same plan",
        answer: "The tool use was rejected",
      }],
      inTok: 0,
      outTok: 0,
      cacheRead: 0,
      cacheCreate: 0,
    }];

    const patched = patchAnswers(turns, {
      "plan-approved": "User has approved your plan. You can now start coding.",
    });

    expect(patched[0].parts?.[0].answer).toContain("approved");
  });

  it("keeps the same array when the authoritative answer is already applied", () => {
    const answer = "User has approved your plan.";
    const turns = [{
      role: "assistant",
      text: "",
      parts: [{ kind: "plan", qid: "plan-approved", plan: "# Same plan", answer }],
      inTok: 0,
      outTok: 0,
      cacheRead: 0,
      cacheCreate: 0,
    }];

    expect(patchAnswers(turns, { "plan-approved": answer })).toBe(turns);
  });
});
