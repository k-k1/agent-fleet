import { describe, expect, it } from "vitest";

import {
  buildClaudeSeq,
  buildClaudeSubmit,
  buildMenuSeq,
  buildRespondAnswers,
  buildSinglePickKeys,
  type KeyStep,
  type QKQuestion,
} from "./questionKeys.ts";

// These sequences are the ONLY contract between the chat card and each agent's TUI
// modal, and a wrong one fails silently — it picks a neighbouring option or drops the
// typed text rather than erroring. Each case below mirrors a sequence verified against
// the real terminal, so a future edit to the shared builders can't quietly reshape one
// agent's modal driving while fixing another's.

const opts = (...labels: string[]) => ({ options: labels.map((label) => ({ label })) });
const q3: QKQuestion = opts("赤", "青", "緑");
// Compact rendering so a failure diff reads as the actual keystrokes.
const show = (seq: KeyStep[]) => seq.map((s) => (s.t !== undefined ? `type:${s.t}` : s.k)).join(" ");

describe("buildSinglePickKeys (single-select single question — every agent)", () => {
  it("keys the first option with a bare Enter, no stray Down", () => {
    expect(buildSinglePickKeys(0)).toEqual(["Enter"]);
  });
  it("moves Down exactly to the option index", () => {
    expect(buildSinglePickKeys(2)).toEqual(["Down", "Down", "Enter"]);
  });
});

describe("buildClaudeSubmit (what the submit button sends)", () => {
  // The card no longer answers on the click; the keys it sends must not have changed
  // with it. A single-select single question keeps the click-verified sequence — routing
  // it through the full modal walk would append the review page's Enter, which on this
  // form lands in the composer after the modal has already closed.
  it("keeps the click-verified keys for a single-select single question", () => {
    expect(buildClaudeSubmit([q3], [["緑"]], [""])).toEqual({ keys: buildSinglePickKeys(2) });
  });

  it("takes the full modal walk when that question is answered with free text", () => {
    const out = buildClaudeSubmit([q3], [[]], ["紫がいい"]);
    expect(out.keys).toBeUndefined();
    expect(show(out.seq!)).toBe(show(buildClaudeSeq([q3], [[]], ["紫がいい"])));
  });

  it("takes the full modal walk for multi-select and for multiple questions", () => {
    expect(buildClaudeSubmit([{ ...q3, multiSelect: true }], [["緑"]], [""]).keys).toBeUndefined();
    expect(buildClaudeSubmit([q3, opts("A", "B")], [["緑"], ["B"]], ["", ""]).keys).toBeUndefined();
  });

  it("falls back to the walk when the pick is stale or missing (no keys invented)", () => {
    expect(buildClaudeSubmit([q3], [["黄"]], [""]).keys).toBeUndefined();
    expect(buildClaudeSubmit([q3], [[]], [""]).keys).toBeUndefined();
  });
});

describe("buildMenuSeq — agy write-in row", () => {
  it("a single-question menu submits exactly what the click used to send", () => {
    // Same invariant as buildClaudeSubmit above, for the menus: deferring the send to
    // the button must not reshape the sequence (here it already coincides).
    expect(show(buildMenuSeq([q3], [["緑"]], [""]))).toBe(buildSinglePickKeys(2).join(" "));
  });

  // agy 1.1.4 実測: the "Write-in..." row sits just after the options and must be
  // ENTERED first (Enter opens "Your answer:"), then the text, then Enter submits.
  it("enters the write-in row before typing, and submits with a trailing Enter", () => {
    const seq = buildMenuSeq([q3], [[]], ["紫がいい"], true);
    expect(show(seq)).toBe("Down Down Down Enter type:紫がいい Enter");
  });

  it("lands on the row AFTER the last option (row index === options.length)", () => {
    // The count of Downs is what puts the cursor on "Write-in..." rather than on the
    // last real option — off by one here silently answers 緑 instead.
    const seq = buildMenuSeq([q3], [[]], ["x"], true);
    expect(seq.filter((s) => s.k === "Down")).toHaveLength(3);
  });

  it("prefers the write-in text over a stale option selection", () => {
    // The card clears the radio when free text is typed; if both ever arrive, the text
    // must win — sending the option would discard what the user actually wrote.
    const seq = buildMenuSeq([q3], [["青"]], ["紫"], true);
    expect(show(seq)).toBe("Down Down Down Enter type:紫 Enter");
  });

  it("falls back to the option sequence when the write-in box is blank or whitespace", () => {
    expect(show(buildMenuSeq([q3], [["青"]], ["   "], true))).toBe("Down Enter");
  });
});

describe("buildMenuSeq — codex / opencode (no verified write-in row)", () => {
  it("answers a single question by Down×index + Enter", () => {
    expect(show(buildMenuSeq([q3], [["緑"]], [""]))).toBe("Down Down Enter");
  });

  it("NEVER types free text when writeIn is off", () => {
    // Guard for codex/opencode: their menus have no verified write-in row, so typed
    // text must not be emitted — on an option row it is ignored and the Enter would
    // confirm whatever is highlighted.
    const seq = buildMenuSeq([q3], [["青"]], ["自由入力"]);
    expect(seq.some((s) => s.t !== undefined)).toBe(false);
    expect(show(seq)).toBe("Down Enter");
  });

  it("concatenates pages for a multi-question form (codex paging resets the cursor)", () => {
    const seq = buildMenuSeq([q3, opts("A", "B")], [["緑"], ["B"]], ["", ""]);
    expect(show(seq)).toBe("Down Down Enter Down Enter");
  });

  it("defaults to the first option when the selected label is no longer offered", () => {
    // findIndex returns -1 for a stale label; clamping to 0 keeps the sequence on a
    // real row instead of emitting a negative number of Downs.
    expect(show(buildMenuSeq([q3], [["黄"]], [""]))).toBe("Enter");
  });
});

describe("buildClaudeSeq — AskUserQuestion tabbed modal", () => {
  it("selects a single-select option and closes on the review page", () => {
    // Trailing Enter activates "Submit answers" on claude's review page — the menus
    // have no such page, which is the core shape difference between the two builders.
    expect(show(buildClaudeSeq([q3], [["青"]], [""]))).toBe("Down Enter Enter");
  });

  it("types single-select free text directly into the row (no Enter to enter it)", () => {
    // claude's free-text row IS the input field — unlike agy's, which must be entered
    // first. Typing lands, then Enter confirms and auto-advances.
    expect(show(buildClaudeSeq([q3], [[]], ["紫"]))).toBe("Down Down Down type:紫 Enter Enter");
  });

  it("toggles multi-select options in place, then advances with Right", () => {
    const q: QKQuestion = { ...opts("A", "B", "C"), multiSelect: true };
    expect(show(buildClaudeSeq([q], [["A", "C"]], [""]))).toBe("Enter Down Down Enter Right Enter");
  });

  it("does NOT press Enter after typing on a multi-select row", () => {
    // Regression: Enter on a multi-select custom row toggles the auto-checked entry back
    // OFF, silently losing the text. One Down exits the field to Submit instead.
    const q: QKQuestion = { ...opts("A", "B"), multiSelect: true };
    const seq = buildClaudeSeq([q], [["A"]], ["custom"]);
    // A(row 0) toggled in place, then 2 Downs to the "Type something" row (index 2).
    expect(show(seq)).toBe("Enter Down Down type:custom Down Enter Enter");
    const afterType = seq[seq.findIndex((s) => s.t !== undefined) + 1];
    expect(afterType).toEqual({ k: "Down" });
  });

  it("walks multi-select toggles relative to the cursor, not from the top each time", () => {
    // The cursor stays put after a toggle, so each hop is (index - cursor) Downs.
    const q: QKQuestion = { ...opts("A", "B", "C", "D"), multiSelect: true };
    expect(show(buildClaudeSeq([q], [["B", "D"]], [""]))).toBe("Down Enter Down Down Enter Right Enter");
  });

  it("emits one page per question and a single trailing submit", () => {
    const seq = buildClaudeSeq([q3, opts("A", "B")], [["青"], ["B"]], ["", ""]);
    expect(show(seq)).toBe("Down Enter Down Enter Enter");
  });

  it("sorts multi-select indices so the cursor only ever moves down", () => {
    // Selection order follows clicks, not option order; walking them unsorted would
    // need negative Downs and land on the wrong rows.
    const q: QKQuestion = { ...opts("A", "B", "C"), multiSelect: true };
    expect(show(buildClaudeSeq([q], [["C", "A"]], [""]))).toBe("Enter Down Down Enter Right Enter");
  });
});

describe("free text folded to one line (TUI paths only)", () => {
  // A {t} step reaches the pane through `tmux send-keys -l`, which writes the bytes
  // verbatim — a newline arrives as a raw LF and acts as Enter on the single-line
  // "Type something" field, so the rest of the sequence (Enter / Down+Enter) then lands
  // on a row that is no longer there. That is the "multi-line answer behaves differently
  // every time" bug; folding at the builder is what keeps the sequence's assumption true.
  const controlish = /[\u0000-\u001f\u007f\u2028\u2029]/;
  const typed = (seq: KeyStep[]) => seq.filter((s) => s.t !== undefined).map((s) => s.t as string);

  it("folds newlines in a single-select answer into single spaces", () => {
    const seq = buildClaudeSeq([q3], [[]], ["一行目\n二行目"]);
    expect(show(seq)).toBe("Down Down Down type:一行目 二行目 Enter Enter");
  });

  it("collapses blank lines and the whitespace around a fold", () => {
    expect(typed(buildClaudeSeq([q3], [[]], ["a\n\n  b \r\n c"]))).toEqual(["a b c"]);
  });

  it("folds a multi-select custom entry too, keeping the Down-not-Enter exit", () => {
    const q: QKQuestion = { ...opts("A", "B"), multiSelect: true };
    expect(show(buildClaudeSeq([q], [["A"]], ["x\ny"]))).toBe("Enter Down Down type:x y Down Enter Enter");
  });

  it("folds agy's write-in text", () => {
    expect(typed(buildMenuSeq([q3], [[]], ["紫\nがいい"], true))).toEqual(["紫 がいい"]);
  });

  it("never emits a control character — tab and ESC would drive the modal, not type", () => {
    // A pasted answer can carry a TAB (focus move) or an ESC (dismisses the whole
    // question); neither can be typed into the field, so both fold like a newline.
    const seq = buildClaudeSeq([q3], [[]], ["a\tb\u001bc"]);
    expect(typed(seq)).toEqual(["a b c"]);
    expect(seq.every((s) => s.t === undefined || !controlish.test(s.t))).toBe(true);
  });

  it("treats a newline-only answer as blank and falls back to the option", () => {
    expect(show(buildClaudeSeq([q3], [["青"]], ["\n \n"]))).toBe("Down Enter Enter");
  });

  it("leaves managed answers untouched — structured JSON carries newlines fine", () => {
    expect(buildRespondAnswers([q3], [[]], ["一行目\n二行目"])).toEqual([{ text: "一行目\n二行目" }]);
  });
});

describe("buildRespondAnswers — managed sessions (no modal)", () => {
  it("returns option indices in ascending order", () => {
    expect(buildRespondAnswers([q3], [["緑", "赤"]], [""])).toEqual([{ options: [0, 2] }]);
  });

  it("carries free text and omits empty fields entirely", () => {
    expect(buildRespondAnswers([q3], [[]], ["紫"])).toEqual([{ text: "紫" }]);
    expect(buildRespondAnswers([q3], [[]], [""])).toEqual([{}]);
  });

  it("drops labels that are no longer offered instead of sending a -1 index", () => {
    expect(buildRespondAnswers([q3], [["黄", "青"]], [""])).toEqual([{ options: [1] }]);
  });

  it("emits one entry per question, in order", () => {
    expect(buildRespondAnswers([q3, opts("A", "B")], [["赤"], ["B"]], ["", ""])).toEqual([
      { options: [0] },
      { options: [1] },
    ]);
  });
});
