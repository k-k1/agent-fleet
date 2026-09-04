import { describe, expect, it } from "vitest";
import { parseQuestionAnswers, resolveAnswer } from "./questionAnswers.ts";

describe("parseQuestionAnswers", () => {
  it("keeps quotes typed inside a free-text answer (the truncation bug)", () => {
    // Verbatim from a real transcript: the answer contains a quoted phrase of its own, and the
    // card used to stop at that first quote.
    const raw =
      'The user answered: "どこまで進めますか？"="A+Bでいこう。ただし"二重再開の回避"は' +
      "セッションエラー→エージェント→セッションにメッセージ送信でトークンコストがかかるので、" +
      'arm済みでassistantAutoResume ON の場合でも自動再試行し打ち切ったときだけアシスタントに報告するようにできないか。". ' +
      "Read the answers carefully — they may request clarification, changes, or that you not proceed — and follow what they actually say.";
    expect(parseQuestionAnswers(raw, ["どこまで進めますか？"])).toEqual([
      'A+Bでいこう。ただし"二重再開の回避"はセッションエラー→エージェント→セッションにメッセージ送信でトークンコストがかかるので、' +
        "arm済みでassistantAutoResume ON の場合でも自動再試行し打ち切ったときだけアシスタントに報告するようにできないか。",
    ]);
  });

  it("splits multiple questions on their own prompts, not on quote counting", () => {
    const raw =
      'Your questions have been answered: "好きな色は？"="青は"空"の色", "好きな動物は？"="鳥". ' +
      "You can now continue with these answers in mind.";
    expect(parseQuestionAnswers(raw, ["好きな色は？", "好きな動物は？"])).toEqual(['青は"空"の色', "鳥"]);
  });

  it("handles a prompt with regex metacharacters and an answer ending in a quote", () => {
    const raw = 'The user answered: "Use (a|b)?"="he said "yes"". Read the answers carefully.';
    expect(parseQuestionAnswers(raw, ["Use (a|b)?"])).toEqual(['he said "yes"']);
  });

  it("drops the option preview appended after the last answer", () => {
    // The SHAPE is verbatim from a real transcript: an option with a `preview` gets
    // `" selected preview:\n<mockup>` glued on after the closing quote. The mockup's own
    // quotes used to end up in the answer, so the picked option matched nothing.
    // Identifiers inside it are dummies — a fixture copied straight out of a real
    // session is how real names end up in a public repo (docs/log/35 §35.7.5).
    const raw =
      'Your questions have been answered: "`branch = develop/3.0.x` はどうする？"="branch も release/3.0.3 に" selected preview:\n' +
      '# .gitmodules\n[submodule "example-core"]\n\tbranch = release/3.0.3. You can now continue with these answers in mind.';
    expect(parseQuestionAnswers(raw, ["`branch = develop/3.0.x` はどうする？"])).toEqual(["branch も release/3.0.3 に"]);
  });

  it("drops a preview on a middle answer without eating the next one", () => {
    const raw =
      'Your questions have been answered: "様式は？"="左レール（推奨）" selected preview:\n┌───┬───┐\n│ a │ b │\n└───┴───┘, ' +
      '"色は？"="青". You can now continue with these answers in mind.';
    expect(parseQuestionAnswers(raw, ["様式は？", "色は？"])).toEqual(["左レール（推奨）", "青"]);
  });

  it("reads a notes-only answer (previewed question's free text) and keeps the LATER answers on their own questions", () => {
    // Verbatim from a real claude 2.1.234 transcript: q1 is answered by free text on a
    // previewed question, so it comes back UNQUOTED as `=(no option selected) notes: …`. The
    // old anchor demanded a quote there, failed for the whole card, and the legacy pair regex —
    // which can't see the unquoted pair — shifted the remaining answers up: q1 showed q2's
    // answer, q2 showed q3's, and q3 was empty.
    const raw =
      'The user answered: "定義の表現形式は？"=(no option selected) notes: コスト優先で決めたい, ' +
      '"検証の入口は？"="UI" selected preview:\n[検証] ボタン, "移行の順序は？"="先に基盤". ' +
      "Read the answers carefully — they may request clarification, changes, or that you not proceed — and follow what they actually say.";
    expect(parseQuestionAnswers(raw, ["定義の表現形式は？", "検証の入口は？", "移行の順序は？"])).toEqual([
      "コスト優先で決めたい",
      "UI",
      "先に基盤",
    ]);
  });

  it("cuts claude's trailing sentence off a notes-only LAST answer (no closing quote to stop at)", () => {
    // Also verbatim (single-question probe): with no quote around the value, the trailer
    // is the only end marker there is.
    const raw =
      'The user answered: "どの方式にしますか？"=(no option selected) notes: コストが理由でこれにしたい. ' +
      "Read the answers carefully — they may request clarification, changes, or that you not proceed — and follow what they actually say.";
    expect(parseQuestionAnswers(raw, ["どの方式にしますか？"])).toEqual(["コストが理由でこれにしたい"]);
  });

  it("a notes answer resolves as free text, never as a pick", () => {
    // The card must show the user's words in the free-text slot with no radio checked —
    // "(no option selected)" itself is claude's bookkeeping, not something to display.
    const answer = parseQuestionAnswers(
      'The user answered: "方式は？"=(no option selected) notes: 安いほうで. You can now continue.',
      ["方式は？"],
    )[0];
    expect(resolveAnswer(answer, ["JSON", "UI"])).toEqual({ chosen: [], extras: ["安いほうで"] });
  });

  it("falls back to pair matching when the prompt is not in the result", () => {
    const raw = 'Your questions have been answered: "old wording"="青". You can now continue.';
    expect(parseQuestionAnswers(raw, ["new wording"])).toEqual(["青"]);
  });

  it("returns [] for an unparseable result so the caller can show it raw", () => {
    expect(parseQuestionAnswers("(no reply captured)", ["Q?"])).toEqual([]);
    expect(parseQuestionAnswers("", ["Q?"])).toEqual([]);
  });
});

describe("resolveAnswer", () => {
  const labels = ["A+B を実装（推奨）", "A だけ実装", "バッファ上限/メモリ保護"];

  it("checks an exactly matching option", () => {
    expect(resolveAnswer("A だけ実装", labels)).toEqual({ chosen: ["A だけ実装"], extras: [] });
  });

  it("keeps free-text prose verbatim, commas and all", () => {
    const a = "A+Bでいこう。ただし、再開は自動で、打ち切りだけ報告して。";
    expect(resolveAnswer(a, labels)).toEqual({ chosen: [], extras: [a] });
  });

  it("combines checked options with a custom entry (multi-select)", () => {
    expect(resolveAnswer("A だけ実装、あとでBも", labels)).toEqual({
      chosen: ["A だけ実装"],
      extras: ["あとでBも"],
    });
  });

  it("matches a label that itself contains a comma / slash", () => {
    expect(resolveAnswer("バッファ上限/メモリ保護", labels)).toEqual({
      chosen: ["バッファ上限/メモリ保護"],
      extras: [],
    });
    expect(resolveAnswer("A, B", ["A, B"])).toEqual({ chosen: ["A, B"], extras: [] });
  });

  it("does not check an option merely contained in the reply", () => {
    expect(resolveAnswer("AWSは使わない", ["AWS"])).toEqual({ chosen: [], extras: ["AWSは使わない"] });
  });
});
