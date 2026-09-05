import { describe, expect, it } from "vitest";
import { awaitingReply, confirmedWorkEnd, latestWorkPromptIndex, textOfParts, workSplit } from "./mirrorParts.ts";

describe("workSplit", () => {
  it("splits at the last tool: work before it, final answer after it", () => {
    const parts = [
      { kind: "text", text: "調べます" },
      { kind: "tool" },
      { kind: "text", text: "追加確認します" },
      { kind: "tool" },
      { kind: "text", text: "修正できました" },
      { kind: "userfile" },
    ];
    expect(workSplit(parts)).toEqual({ at: 4, tools: 2, responses: 2 });
    expect(textOfParts(parts.slice(4))).toBe("修正できました");
  });

  it("treats a question and a plan as work boundaries too", () => {
    expect(workSplit([{ kind: "question" }, { kind: "text", text: "回答です" }])).toEqual({
      at: 1,
      tools: 1,
      responses: 0,
    });
    expect(workSplit([{ kind: "plan" }, { kind: "text", text: "完了しました" }])?.at).toBe(1);
  });

  it("does not split with no tool, or before the final text arrives", () => {
    expect(workSplit([{ kind: "text", text: "通常回答" }])).toBeNull();
    expect(workSplit([{ kind: "text", text: "確認中" }, { kind: "tool" }])).toBeNull();
    expect(workSplit([{ kind: "tool" }, { kind: "thinking", text: "推論" }])).toBeNull();
  });

  it("keeps the real answer as the final answer even when a wrap-up tool and a short follow-up trail it", () => {
    const parts = [
      { kind: "text", text: "調べます" },
      { kind: "tool" },
      { kind: "text", text: "ここが実際の最終回答で、いちばん長い本文になります。" },
      { kind: "tool" }, // memory write (wrap-up)
      { kind: "text", text: "メモしました" },
    ];
    // The boundary is the real answer (2), not just after the trailing tool (4); the wrap-up tool
    // and its follow-up stay on the final-answer side.
    expect(workSplit(parts)).toEqual({ at: 2, tools: 1, responses: 1 });
    expect(textOfParts(parts.slice(2))).toBe(
      "ここが実際の最終回答で、いちばん長い本文になります。\n\nメモしました",
    );
  });

  it("keeps the trailing text when it is no shorter than the previous one (narration, tool, answer)", () => {
    const parts = [
      { kind: "text", text: "確認します" },
      { kind: "tool" },
      { kind: "text", text: "短い経過" },
      { kind: "tool" },
      { kind: "text", text: "これが最終的な回答の本文です。" },
    ];
    expect(workSplit(parts)).toEqual({ at: 4, tools: 2, responses: 2 });
  });

  it("strips several wrap-up tools in a row", () => {
    const parts = [
      { kind: "tool" },
      { kind: "text", text: "十分に長い実際の最終回答の本文です。" },
      { kind: "tool" }, // wrap-up 1
      { kind: "text", text: "追記" },
      { kind: "tool" }, // wrap-up 2
      { kind: "text", text: "完了" },
    ];
    expect(workSplit(parts)).toEqual({ at: 1, tools: 1, responses: 0 });
  });

  it("returns the work confirmed up to the last tool without waiting for the final answer", () => {
    expect(confirmedWorkEnd([{ kind: "text" }, { kind: "tool" }, { kind: "text" }])).toBe(2);
    expect(confirmedWorkEnd([{ kind: "text" }])).toBe(0);
  });
});

describe("latestWorkPromptIndex", () => {
  it("treats a just-sent pending turn as the current work boundary", () => {
    const groups = [
      { role: "user" },
      { role: "assistant" },
      { role: "user", pending: true },
    ];
    expect(latestWorkPromptIndex(groups)).toBe(2);
  });

  it("does not treat an unexecuted queued prompt as the current work boundary", () => {
    const groups = [
      { role: "user" },
      { role: "assistant" },
      { role: "user", queued: true },
    ];
    expect(latestWorkPromptIndex(groups)).toBe(0);
  });
});

describe("awaitingReply", () => {
  it("is true while the latest user turn has no reply yet, holding the spinner from idle to render", () => {
    // Just after sending: the reply turn is not in the transcript yet, which would otherwise show
    // a blank gap.
    expect(awaitingReply([{ role: "user" }, { role: "assistant" }, { role: "user" }])).toBe(true);
  });

  it("is false once an assistant block arrives, dropping the spinner and showing the answer", () => {
    expect(awaitingReply([{ role: "user" }, { role: "assistant" }])).toBe(false);
    expect(awaitingReply([{ role: "user" }, { role: "assistant" }, { role: "user" }, { role: "assistant" }])).toBe(false);
  });

  it("does not wait when there is no prompt at all (new session or history only)", () => {
    expect(awaitingReply([])).toBe(false);
    expect(awaitingReply([{ role: "assistant" }])).toBe(false);
  });
});
