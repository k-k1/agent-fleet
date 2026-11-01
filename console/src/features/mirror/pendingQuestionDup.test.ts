import { describe, expect, it } from "vitest";
import { hidePendingQuestionDup } from "./pendingQuestionDup.ts";
import type { Question, Turn } from "./transcript/types.ts";

const q: Question[] = [
  { header: "方式", question: "どれ？", multiSelect: true, options: [{ label: "A", preview: "x" }, { label: "B" }] },
];
const qPart = (qid: string, answer?: string) => ({ kind: "question", qid, questions: q, ...(answer ? { answer } : {}) });
const turn = (idx: number, parts: Turn["parts"]): Turn => ({ role: "assistant", idx, parts });

describe("hidePendingQuestionDup", () => {
  it("hides the unanswered part and drops a turn left empty", () => {
    const turns = [turn(1, [{ kind: "text", text: "hi" }]), turn(2, [qPart("q1")])];
    const out = hidePendingQuestionDup(turns, q);
    expect(out).toHaveLength(1);
    expect(out[0].idx).toBe(1);
  });

  it("keeps the other parts of the turn", () => {
    const out = hidePendingQuestionDup([turn(2, [{ kind: "text", text: "hi" }, qPart("q1")])], q);
    expect(out[0].parts).toEqual([{ kind: "text", text: "hi" }]);
  });

  it("matches despite the managed id and empty optional fields", () => {
    const pend: Question[] = [{ id: "i1", header: "方式", question: "どれ？", multiSelect: true, options: [{ label: "A", description: "", preview: "x" }, { label: "B" }] }];
    expect(hidePendingQuestionDup([turn(2, [qPart("q1")])], pend)).toHaveLength(0);
  });

  it("asked again with identical content: hides only the new one, keeps the decided one", () => {
    const turns = [turn(2, [qPart("q1", "A")]), turn(9, [qPart("q2")])];
    const out = hidePendingQuestionDup(turns, q);
    expect(out.map((t) => t.idx)).toEqual([2]);
  });

  it("never hides an answered newest copy (stale payload)", () => {
    const turns = [turn(2, [qPart("q1", "A")])];
    expect(hidePendingQuestionDup(turns, q)).toBe(turns);
  });

  it("leaves a different question alone", () => {
    const turns = [turn(2, [qPart("q1")])];
    expect(hidePendingQuestionDup(turns, [{ question: "別" }])).toBe(turns);
  });

  it("is a no-op without a pending question", () => {
    const turns = [turn(2, [qPart("q1")])];
    expect(hidePendingQuestionDup(turns, null)).toBe(turns);
    expect(hidePendingQuestionDup(turns, [])).toBe(turns);
  });

  it("does not touch plan parts", () => {
    const turns = [turn(2, [{ kind: "plan", plan: "p", qid: "p1" }])];
    expect(hidePendingQuestionDup(turns, q)).toBe(turns);
  });
});
