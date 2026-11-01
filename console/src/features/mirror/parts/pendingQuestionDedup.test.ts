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
      { role: "assistant", idx: 1, parts: [{ kind: "text", text: "hi" }, { kind: "question", tool: "AskUserQuestion", questions: [q("one")] }] },
    ];
    const out = dropPendingQuestionPart(turns, [q("one")]);
    expect(out).toHaveLength(1);
    expect(out[0].parts).toEqual([{ kind: "text", text: "hi" }]);
    expect(turns[0].parts).toHaveLength(2); // input untouched: the card must come back once answered
  });

  it("leaves other agents' question tools alone", () => {
    for (const tool of ["request_user_input", "question", ""]) {
      const turns = [askTurn(1, [q("one")], { tool })];
      expect(dropPendingQuestionPart(turns, [q("one")])).toBe(turns);
    }
  });

  it("keeps an already answered or declined card", () => {
    expect(dropPendingQuestionPart([askTurn(1, [q("one")], { answer: "A" })], [q("one")])).toHaveLength(1);
    expect(dropPendingQuestionPart([askTurn(1, [q("one")], { answer: "x", declined: true })], [q("one")])).toHaveLength(1);
  });

  it("leaves other questions alone and hides only the trailing identical one", () => {
    const turns = [askTurn(1, [q("one")], { answer: "A" }), askTurn(2, [q("two")], { answer: "B" }), askTurn(3, [q("one")])];
    const out = dropPendingQuestionPart(turns, [q("one")]);
    expect(out.map((t) => t.idx)).toEqual([1, 2]);
  });

  it("keeps an older identical open question when the new ask was already hidden by the Agent", () => {
    // The first ask was abandoned (no answer ever landed); the re-ask's part never reached the
    // client because the Agent hid it. Later turns sit behind the old part, so it is history.
    const turns: Turn[] = [
      askTurn(1, [q("one")]),
      { role: "user", idx: 2, parts: [{ kind: "text", text: "again please" }] },
      { role: "assistant", idx: 3, parts: [{ kind: "text", text: "asking once more" }] },
    ];
    expect(dropPendingQuestionPart(turns, [q("one")])).toBe(turns);
  });

  it("is a no-op without a pending question or a match", () => {
    const turns = [askTurn(1, [q("one")])];
    expect(dropPendingQuestionPart(turns, null)).toBe(turns);
    expect(dropPendingQuestionPart(turns, [])).toBe(turns);
    expect(dropPendingQuestionPart(turns, [q("two")])).toBe(turns);
    expect(dropPendingQuestionPart(turns, [q("one", { options: [{ label: "A" }] })])).toBe(turns);
  });
});
