// What must hold for a question-card draft, both directions:
//   - it comes back for the SAME form (the point: a tab switch unmounts the card), and
//   - it never comes back for another one, because a pre-filled answer to a question the
//     user has not read is one submit click away from being sent.
import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  questionSig,
  readQuestionDraft,
  writeQuestionDraft,
  clearQuestionDraft,
  questionDraftKey,
  carriedDraftKey,
  siblingDraftKey,
} from "./questionDraft.ts";
import type { Question } from "./transcript/types.ts";

// vitest runs the node project without a DOM (vite.config.js), so stub the storage.
const store = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => void store.set(k, v),
  removeItem: (k: string) => void store.delete(k),
});

const QS: Question[] = [
  { question: "どっち？", options: [{ label: "A" }, { label: "B" }] },
  { question: "どれ？", multiSelect: true, options: [{ label: "X" }, { label: "Y" }] },
];
const save = (qs: Question[], sel: string[][], text: string[]) => writeQuestionDraft("k", questionSig(qs), sel, text);

describe("question draft storage", () => {
  beforeEach(() => store.clear());

  it("restores the selection and the free text of the same form", () => {
    save(QS, [["B"], ["X", "Y"]], ["", "そのほか"]);
    expect(readQuestionDraft("k", QS)).toEqual({ sel: [["B"], ["X", "Y"]], freeText: ["", "そのほか"] });
  });

  it("keys are per session and never shared between the pending and carried cards", () => {
    expect(questionDraftKey("s1")).not.toBe(carriedDraftKey("s1"));
    expect(questionDraftKey("s1")).not.toBe(questionDraftKey("s2"));
    expect(questionDraftKey(null)).toBeNull();
    expect(carriedDraftKey("")).toBeNull();
  });

  it("pairs the two cards of one session, and only those", () => {
    expect(siblingDraftKey(questionDraftKey("s1"))).toBe(carriedDraftKey("s1"));
    expect(siblingDraftKey(carriedDraftKey("s1"))).toBe(questionDraftKey("s1"));
    expect(siblingDraftKey(null)).toBeNull();
    expect(siblingDraftKey("af.mirror-draft.s1")).toBeNull(); // the composer's draft, not a card's
  });

  it("hands the draft over when the stopped session turns the card into the carried one", () => {
    // The reported loss: typed into the live card's free-text row, the session stops, and
    // the carried card — the same question, answered as prose — came up empty.
    writeQuestionDraft(questionDraftKey("s1"), questionSig(QS), [["B"], []], ["", "そのほか"]);
    expect(readQuestionDraft(carriedDraftKey("s1"), QS)).toEqual({ sel: [["B"], []], freeText: ["", "そのほか"] });
    // And back, for the question the resumed agent asks again.
    store.clear();
    writeQuestionDraft(carriedDraftKey("s1"), questionSig(QS), [[], []], ["書きかけ", ""]);
    expect(readQuestionDraft(questionDraftKey("s1"), QS)?.freeText).toEqual(["書きかけ", ""]);
  });

  it("hands over nothing to another session, or to another question", () => {
    writeQuestionDraft(questionDraftKey("s1"), questionSig(QS), [["B"], []], ["", ""]);
    expect(readQuestionDraft(carriedDraftKey("s2"), QS)).toBeNull();
    const other: Question[] = [{ ...QS[0], question: "べつの質問" }, QS[1]];
    expect(readQuestionDraft(carriedDraftKey("s1"), other)).toBeNull();
  });

  it("the card on screen takes the form over, so a cleared row stays cleared", () => {
    const pending = questionDraftKey("s1");
    const carried = carriedDraftKey("s1");
    writeQuestionDraft(pending, questionSig(QS), [[], []], ["消す前", ""]);
    writeQuestionDraft(carried, questionSig(QS), [[], []], ["消す前", ""]); // the carried card restored it
    expect(store.has(pending!)).toBe(false); // …and now owns it
    writeQuestionDraft(carried, questionSig(QS), [[], []], ["", ""]); // the user emptied the row
    expect(readQuestionDraft(carried, QS)).toBeNull();
  });

  it("an unrelated draft of the other card is left alone", () => {
    const other: Question[] = [{ question: "べつの質問", options: [{ label: "A" }] }];
    writeQuestionDraft(carriedDraftKey("s1"), questionSig(other), [["A"]], [""]);
    writeQuestionDraft(questionDraftKey("s1"), questionSig(QS), [["B"], []], ["", ""]);
    expect(readQuestionDraft(carriedDraftKey("s1"), other)).not.toBeNull();
  });

  it("an answered card takes the other's copy of that form with it", () => {
    writeQuestionDraft(questionDraftKey("s1"), questionSig(QS), [["B"], []], ["", "そのほか"]);
    clearQuestionDraft(carriedDraftKey("s1"), questionSig(QS)); // sent from the carried card
    expect(readQuestionDraft(carriedDraftKey("s1"), QS)).toBeNull();
  });

  it("a null key stores nothing and reads back nothing", () => {
    writeQuestionDraft(null, questionSig(QS), [["B"], []], ["", ""]);
    expect(store.size).toBe(0);
    expect(readQuestionDraft(null, QS)).toBeNull();
  });

  it("drops the draft when the question, the options or multiSelect changed", () => {
    save(QS, [["B"], ["X"]], ["", ""]);
    const reworded: Question[] = [{ ...QS[0], question: "どちらにする？" }, QS[1]];
    const fewer: Question[] = [{ ...QS[0], options: [{ label: "A" }] }, QS[1]];
    const single: Question[] = [QS[0], { ...QS[1], multiSelect: false }];
    expect(readQuestionDraft("k", reworded)).toBeNull();
    expect(readQuestionDraft("k", fewer)).toBeNull();
    expect(readQuestionDraft("k", single)).toBeNull();
    expect(readQuestionDraft("k", QS)).not.toBeNull(); // the control: the tool does catch a match
  });

  it("a managed question re-asked under a new interaction id is a different form", () => {
    const first: Question[] = [{ id: "i1", question: "どっち？", options: [{ label: "A" }] }];
    const again: Question[] = [{ id: "i2", question: "どっち？", options: [{ label: "A" }] }];
    save(first, [["A"]], [""]);
    expect(readQuestionDraft("k", again)).toBeNull();
  });

  it("keeps the draft when only a description or a preview was reworded", () => {
    // Those are reading material, not what the answer is made of — losing a draft to a
    // redrawn card would be the very bug this module fixes.
    const dressed: Question[] = [
      { question: "どっち？", options: [{ label: "A", description: "説明" }, { label: "B", preview: "mock" }] },
      QS[1],
    ];
    save(QS, [["B"], []], ["", ""]);
    expect(readQuestionDraft("k", dressed)?.sel).toEqual([["B"], []]);
  });

  it("an empty card removes the entry instead of leaving a husk behind", () => {
    save(QS, [["B"], []], ["", ""]);
    save(QS, [[], []], ["", "   "]); // untoggled it again; whitespace is not an answer
    expect(store.has("k")).toBe(false);
  });

  it("sanitizes what it hands back: unknown labels, wrong shapes, extra questions", () => {
    localStorage.setItem(
      "k",
      JSON.stringify({ sig: questionSig(QS), sel: [["A", "gone"], "nope", ["X"]], freeText: ["ok", 7, "extra"] }),
    );
    // "gone" is no longer offered, the string is not a selection, and the third
    // question does not exist — none of them may reach the key builders, which resolve a
    // label to a Down count.
    expect(readQuestionDraft("k", QS)).toEqual({ sel: [["A"], []], freeText: ["ok", ""] });
  });

  it("a single-select question keeps at most one label", () => {
    localStorage.setItem("k", JSON.stringify({ sig: questionSig(QS), sel: [["A", "B"], []], freeText: ["", ""] }));
    expect(readQuestionDraft("k", QS)?.sel[0]).toEqual(["A"]);
  });

  it("survives a corrupt entry and clears on demand", () => {
    localStorage.setItem("k", "{not json");
    expect(readQuestionDraft("k", QS)).toBeNull();
    save(QS, [["A"], []], ["", ""]);
    clearQuestionDraft("k");
    expect(readQuestionDraft("k", QS)).toBeNull();
    clearQuestionDraft(null); // no throw
  });
});
