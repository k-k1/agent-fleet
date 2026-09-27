import { describe, it, expect } from "vitest";
import { parseQuestionTranslation, questionTranslateSource, translatedQuestions } from "./questionTranslate.ts";
import type { Question } from "./transcript/types.ts";

const QS: Question[] = [
  {
    header: "Live walkthrough",
    question: "How do you want to handle that?\nPick one.",
    options: [
      { label: "I'll redeploy (Recommended)", description: "You rebuild the dev deployment." },
      { label: "Skip it", preview: "```\nmock\n```" },
    ],
  },
];

describe("questionTranslateSource", () => {
  it("marks every non-empty field and leaves previews out", () => {
    expect(questionTranslateSource(QS)).toBe(
      [
        "`Q1.header` Live walkthrough",
        "`Q1.text` How do you want to handle that?\nPick one.",
        "`Q1.O1.label` I'll redeploy (Recommended)",
        "`Q1.O1.desc` You rebuild the dev deployment.",
        "`Q1.O2.label` Skip it",
      ].join("\n\n"),
    );
  });

  it("is empty for a card with nothing to say", () => {
    expect(questionTranslateSource([{ options: [] }])).toBe("");
  });
});

describe("parseQuestionTranslation", () => {
  it("round-trips a translation back onto the same fields, multi-line text included", () => {
    const reply = [
      "`Q1.header` ライブ確認",
      "`Q1.text` どう進めますか？\n1 つ選んでください。",
      "`Q1.O1.label` 再デプロイする（推奨）",
      "`Q1.O1.desc` 開発配備を作り直します。",
      "`Q1.O2.label` 飛ばす",
    ].join("\n\n");
    const got = translatedQuestions(QS, parseQuestionTranslation(reply));
    expect(got[0].header).toBe("ライブ確認");
    expect(got[0].question).toBe("どう進めますか？\n1 つ選んでください。");
    expect(got[0].options!.map((o) => o.label)).toEqual(["再デプロイする（推奨）", "飛ばす"]);
    expect(got[0].options![0].description).toBe("開発配備を作り直します。");
    // The preview is never sent, so it is never replaced.
    expect(got[0].options![1].preview).toBe(QS[0].options![1].preview);
  });

  it("keeps the original for a field whose marker did not survive, and drops a preamble", () => {
    const reply = "翻訳しました。\n\n`Q1.O2.label` 飛ばす\n\n`Q1.O1.label`";
    const got = translatedQuestions(QS, parseQuestionTranslation(reply));
    expect(got[0].header).toBe("Live walkthrough");
    expect(got[0].options!.map((o) => o.label)).toEqual(["I'll redeploy (Recommended)", "飛ばす"]);
  });
});
