import { describe, expect, it } from "vitest";
import type { Question, Turn } from "../transcript/types.ts";
import { dropPendingQuestionPart } from "./pendingQuestionDedup.ts";

const q = (question: string, over: Partial<Question> = {}): Question => ({
  header: "H",
  question,
  multiSelect: true,
  options: [{ label: "A", description: "a" }, { label: "B" }],
  ...over,
});

const askTurn = (idx: number, qs: Question[], extra: object = {}): Turn => ({
  role: "assistant",
  idx,
  parts: [{ kind: "question", tool: "AskUserQuestion", questions: qs, qid: "toolu_" + idx, ...extra }],
});

describe("dropPendingQuestionPart", () => {
  it("drops the open transcript copy of the pending question (part delivered by an earlier poll)", () => {
    const turns = [askTurn(3, [q("one")])];
    // The pending payload may carry a managed Interaction id the transcript part never has.
    expect(dropPendingQuestionPart(turns, [q("one", { id: "int-1" })])).toEqual([]);
  });

  it("keeps the prose of a turn and only strips the question part", () => {
    const turns: Turn[] = [
      { role: "assistant", idx: 1, parts: [{ kind: "text", text: "hi" }, { kind: "question", questions: [q("one")] }] },
    ];
    const out = dropPendingQuestionPart(turns, [q("one")]);
    expect(out).toHaveLength(1);
    expect(out[0].parts).toEqual([{ kind: "text", text: "hi" }]);
    expect(turns[0].parts).toHaveLength(2); // input untouched: the card must come back once answered
  });

  it("keeps an already answered or declined card", () => {
    expect(dropPendingQuestionPart([askTurn(1, [q("one")], { answer: "A" })], [q("one")])).toHaveLength(1);
    expect(dropPendingQuestionPart([askTurn(1, [q("one")], { answer: "x", declined: true })], [q("one")])).toHaveLength(1);
  });

  it("leaves other questions alone and hides only the newest identical one", () => {
    const turns = [askTurn(1, [q("one")], { answer: "A" }), askTurn(2, [q("two")], { answer: "B" }), askTurn(3, [q("one")])];
    const out = dropPendingQuestionPart(turns, [q("one")]);
    expect(out.map((t) => t.idx)).toEqual([1, 2]);
  });

  it("is a no-op without a pending question or a match", () => {
    const turns = [askTurn(1, [q("one")])];
    expect(dropPendingQuestionPart(turns, null)).toBe(turns);
    expect(dropPendingQuestionPart(turns, [])).toBe(turns);
    expect(dropPendingQuestionPart(turns, [q("two")])).toBe(turns);
    expect(dropPendingQuestionPart(turns, [q("one", { options: [{ label: "A" }] })])).toBe(turns);
  });
});
