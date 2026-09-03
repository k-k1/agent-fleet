import { describe, expect, it } from "vitest";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

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
// A question whose options carry a preview — claude's AskUserQuestion drops the
// "Type something" row for these (docs/build/92 §6), which is the shape the free-text
// tests below exercise.
const withPreview = (...labels: string[]): QKQuestion => ({
  options: labels.map((label, i) => (i === 0 ? { label, preview: "モックアップ\n2行目" } : { label })),
});
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
  it("selects a single-select option with no trailing Enter — a single question has no review page", () => {
    // A lone single-select question submits on its own Enter (verified in the real TUI);
    // an extra trailing Enter here would land in the composer after the modal has already
    // closed. Multi-question and multi-select forms DO reach a review page — see below.
    expect(show(buildClaudeSeq([q3], [["青"]], [""]))).toBe("Down Enter");
  });

  it("types single-select free text directly into the row (no Enter to enter it)", () => {
    // claude's free-text row IS the input field — unlike agy's, which must be entered
    // first. Typing lands, then Enter confirms and submits (no review page for one
    // question, so no trailing Enter here either).
    expect(show(buildClaudeSeq([q3], [[]], ["紫"]))).toBe("Down Down Down type:紫 Enter");
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

  it("keeps the trailing Enter for a single MULTI-select question (its Enter only toggles, never submits)", () => {
    // Unlike single-select, a lone multi-select question still reaches a submit step
    // (via Right), so the review-page Enter is not the single-question special case.
    const q: QKQuestion = { ...opts("A", "B"), multiSelect: true };
    expect(show(buildClaudeSeq([q], [[]], [""]))).toBe("Right Enter");
  });
});

describe("buildClaudeSeq — preview options drop the free-text row (docs/build/92 §6)", () => {
  // claude's AskUserQuestion renders single-select options with a `preview` in a wide
  // layout that has NO "Type something" row — replaced by a per-OPTION "n to add notes"
  // field. The old downs(opts.length) math assumed the free-text row still sat right
  // after the last option; with preview it instead lands on the unnumbered "Chat about
  // this" row, which silently swallows the typed text and then Enter activates "Chat
  // about this" — claude reports this back as "User declined to answer questions" /
  // "(No answer provided)". Reported bug (szawtwy, and reproduced live in this session):
  // every preview-bearing free-text answer failed this way regardless of question count.
  const preview: QKQuestion = withPreview("案A", "案B", "案C");

  it("opens notes with 'n' instead of navigating to a Type-something row that doesn't exist", () => {
    // No Down at all: a question's tab always starts with the cursor on option 0, and
    // notes attach without needing to select that option first (実測: submits as
    // "(no option selected) notes: …").
    // 'n' goes out as literal text, NOT as a named key — the Agent rejects any {k} it
    // doesn't know with 400 and then drops the entire sequence (see the whitelist test
    // at the bottom of this file).
    expect(show(buildClaudeSeq([preview], [[]], ["コスト優先で"]))).toBe("type:n type:コスト優先で Enter");
  });

  it("still uses Down×i, Enter for a plain OPTION pick on a previewed question", () => {
    // Only the free-text path changes; picking a listed option never touched the
    // free-text row in the first place.
    expect(show(buildClaudeSeq([preview], [["案B"]], [""]))).toBe("Down Enter");
  });

  it("decides per QUESTION, not once for the whole form", () => {
    // A form can mix a previewed question with a plain one (the real report's shape:
    // 3 questions, only 2 carrying preview) — each question's own options decide which
    // free-text row (if any) applies for that page.
    const seq = buildClaudeSeq([preview, opts("X", "Y")], [[], ["Y"]], ["自由記述", ""]);
    expect(show(seq)).toBe("type:n type:自由記述 Enter Down Enter Enter");
  });

  it("free text on a LATER plain question still uses the old Type-something row, even after an earlier previewed one", () => {
    const seq = buildClaudeSeq([preview, opts("X", "Y")], [["案A"], []], ["", "自由記述"]);
    // q1: previewed, answered by picking the option (no free text) → Down×0, Enter.
    // q2: plain, answered by free text → Down×opts.length(2), type, Enter, then the
    // multi-question review page's trailing Enter.
    expect(show(seq)).toBe("Enter Down Down type:自由記述 Enter Enter");
  });

  it("buildClaudeSubmit routes a previewed single question's free text through the same walk", () => {
    // Mirrors the existing non-preview case: free text always disqualifies the
    // click-verified short path (buildSinglePickKeys), preview or not.
    const out = buildClaudeSubmit([preview], [[]], ["コスト優先で"]);
    expect(out.keys).toBeUndefined();
    expect(show(out.seq!)).toBe(show(buildClaudeSeq([preview], [[]], ["コスト優先で"])));
  });

  it("folds a previewed question's free text the same way as the plain row", () => {
    // The notes field is still a single-line input under the hood (ctrl+g opens Vim for
    // more, but the driven path doesn't use that) — multi-line answers fold like normal.
    expect(show(buildClaudeSeq([preview], [[]], ["一行目\n二行目"]))).toBe("type:n type:一行目 二行目 Enter");
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
    expect(show(seq)).toBe("Down Down Down type:一行目 二行目 Enter");
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
    expect(show(buildClaudeSeq([q3], [["青"]], ["\n \n"]))).toBe("Down Enter");
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

describe("every named key these builders emit is one the Agent accepts", () => {
  // 層またぎの固定。ここのビルダーが吐く {k} は Agent の /sessions/{name}/input が
  // **名前付きキーのホワイトリスト**（session_io.go の allowedKey）で検査し、知らない
  // 名前が1つでも混じると要求ごと 400 bad_key で弾かれる — つまり打鍵は1つも届かない。
  // Console 側は失敗を握り潰していたので、症状は「自由入力してボタンを押しても無反応」
  // になった（2026-08-14 の preview 修正が {k:"n"} を吐き、実 TUI（tmux 直叩き）では
  // 通るのに Console からは一度も届かなかった実例）。モーダルの契約はテストされていても
  // **配送層の契約**は誰も見ていなかったので、ここで結ぶ。
  const allowed = (() => {
    const go = readFileSync(fileURLToPath(new URL("../../../../workspace/agent/internal/sessionx/session_io.go", import.meta.url)), "utf8");
    const fn = go.slice(go.indexOf("func allowedKey("));
    const cases = fn.slice(0, fn.indexOf("}")).match(/"([^"]+)"/g) || [];
    return new Set(cases.map((s) => s.slice(1, -1)));
  })();

  it("parsed the Go whitelist (a rename there must fail loudly here, not silently pass)", () => {
    expect(allowed.has("Down")).toBe(true);
    expect(allowed.has("Enter")).toBe(true);
    expect(allowed.has("n")).toBe(false); // 'n' must ride as {t}, not {k}
  });

  it("holds for every form the card can build", () => {
    const preview = withPreview("案A", "案B", "案C");
    const multi: QKQuestion = { ...q3, multiSelect: true };
    const forms: Array<[QKQuestion[], string[][], string[]]> = [
      [[q3], [["緑"]], [""]],
      [[q3], [[]], ["自由記述"]],
      [[preview], [[]], ["自由記述"]], // ← the reported bug's shape
      [[preview], [["案B"]], [""]],
      [[preview, q3], [[], ["赤"]], ["自由記述", ""]],
      [[preview, q3], [["案A"], []], ["", "自由記述"]],
      [[multi], [["赤", "緑"]], [""]],
      [[multi], [["赤"]], ["自由記述"]],
      [[q3, opts("X", "Y")], [["青"], ["Y"]], ["", ""]],
    ];
    for (const [qs, sel, ft] of forms) {
      const out = buildClaudeSubmit(qs, sel, ft);
      for (const k of out.keys || []) expect([show([{ k }]), allowed.has(k)]).toEqual([k, true]);
      for (const s of out.seq || [])
        if (s.k !== undefined) expect([show([s]), allowed.has(s.k)]).toEqual([s.k, true]);
      // 同じ検査を menu（codex/opencode/agy）経路にも。
      for (const s of buildMenuSeq(qs, sel, ft, true))
        if (s.k !== undefined) expect([show([s]), allowed.has(s.k)]).toEqual([s.k, true]);
    }
  });
});
