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
    const { turns: out, hidden } = hidePendingQuestionDup(turns, q);
    expect(hidden).toBe(true);
    expect(out).toHaveLength(1);
    expect(out[0].idx).toBe(1);
  });

  it("keeps the other parts of the turn", () => {
    const { turns: out } = hidePendingQuestionDup([turn(2, [{ kind: "text", text: "hi" }, qPart("q1")])], q);
    expect(out[0].parts).toEqual([{ kind: "text", text: "hi" }]);
  });

  it("matches despite the managed id and empty optional fields", () => {
    const pend: Question[] = [{ id: "i1", header: "方式", question: "どれ？", multiSelect: true, options: [{ label: "A", description: "", preview: "x" }, { label: "B" }] }];
    expect(hidePendingQuestionDup([turn(2, [qPart("q1")])], pend).turns).toHaveLength(0);
  });

  it("asked again with identical content: hides only the new one, keeps the decided one", () => {
    const turns = [turn(2, [qPart("q1", "A")]), turn(9, [qPart("q2")])];
    const { turns: out } = hidePendingQuestionDup(turns, q);
    expect(out.map((t) => t.idx)).toEqual([2]);
  });

  it("never hides an answered newest copy (stale payload)", () => {
    const turns = [turn(2, [qPart("q1", "A")])];
    expect(hidePendingQuestionDup(turns, q)).toEqual({ turns, hidden: false });
  });

  it("leaves a different question alone", () => {
    const turns = [turn(2, [qPart("q1")])];
    expect(hidePendingQuestionDup(turns, [{ question: "別" }])).toEqual({ turns, hidden: false });
  });

  it("is a no-op without a pending question", () => {
    const turns = [turn(2, [qPart("q1")])];
    expect(hidePendingQuestionDup(turns, null).hidden).toBe(false);
    expect(hidePendingQuestionDup(turns, []).turns).toBe(turns);
  });

  it("does not touch plan parts", () => {
    const turns = [turn(2, [{ kind: "plan", plan: "p", qid: "p1" }])];
    expect(hidePendingQuestionDup(turns, q)).toEqual({ turns, hidden: false });
  });

  it("keeps an older unanswered identical ask when the real one was hidden by the Agent", () => {
    // The newest ask never reached the Console (the Agent hid it in its own poll); later
    // assistant work after the older, unanswered copy proves that copy is not the pending one.
    const turns = [turn(2, [qPart("q1")]), turn(9, [{ kind: "text", text: "continuing" }])];
    expect(hidePendingQuestionDup(turns, q)).toEqual({ turns, hidden: false });
  });

  it("keeps an interrupted older ask when the user moved on and the new ask was hidden by the Agent", () => {
    // Held: [old ask, user follow-up]. The new identical ask was a question-only turn that the
    // Agent dropped whole, so it never reaches the Console.
    const turns = [turn(2, [qPart("q1")]), { role: "user", idx: 3, text: "next" } as Turn];
    expect(hidePendingQuestionDup(turns, q)).toEqual({ turns, hidden: false });
  });

  it("ignores optimistic or queued local turns after the match", () => {
    const turns = [turn(2, [qPart("q1")]), { role: "user", idx: 3, text: "next", queued: true } as Turn];
    const r = hidePendingQuestionDup(turns, q);
    expect(r.hidden).toBe(true);
    expect(r.turns.map((t) => t.idx)).toEqual([3]);
  });
});
