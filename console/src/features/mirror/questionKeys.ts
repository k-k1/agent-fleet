// Key sequences that drive an agent's pending-question modal from the chat card.
//
// Every sequence here was verified against the real TUI (see the per-branch notes);
// the shapes differ per agent because the modals differ, and a wrong sequence does
// not fail loudly — it silently picks the wrong option or drops the typed text. That
// is why these builders live in their own module: they are pure (state in → key
// steps out), so each agent's modal contract is pinned by a test instead of only by
// a comment, and MirrorView keeps just the wiring.
//
// NEVER send an option label as text: every modal ignores typed text on an option row
// and the Enter then confirms the highlighted first option (docs/build/92-driving-a-tui.md).

import type { InteractionAnswer } from "../../core/api/client.ts";
import { previewBody } from "./optionPreview.ts";

// One step of a sequence: a named key (k) or literal text to type (t).
export type KeyStep = { k?: string; t?: string };

export interface QKOption {
  label: string;
  preview?: string;
}
export interface QKQuestion {
  multiSelect?: boolean;
  options?: QKOption[];
}

// Does THIS question's option set carry a preview? claude only ever attaches preview to
// single-select options (the tool's own contract), and whether the row layout below
// changes is decided per question, not once for the whole form — a later question with
// plain options reverts to the normal layout even if an earlier one had previews
// (実測 v2.1.232, docs/build/92 §6).
const hasPreview = (opts: QKOption[]): boolean => opts.some((o) => previewBody(o.preview) !== "");

// The card's per-question state: the labels checked (single-select holds at most one)
// and the free-text answer. Indices are resolved from labels here so a stale label —
// one no longer offered — drops out instead of keying to a wrong row.
const pickedIdx = (opts: QKOption[], labels: string[] | undefined): number[] =>
  (labels || [])
    .map((l) => opts.findIndex((o) => o.label === l))
    .filter((i) => i >= 0)
    .sort((a, b) => a - b);

const trimAt = (freeText: string[], qi: number) => (freeText[qi] || "").trim();

// Free text bound for a TUI modal is folded to ONE line. A {t} step is delivered with
// `tmux send-keys -l`, which puts the bytes on the pane verbatim (実測: a newline
// arrives as a raw LF, not as typed characters) — so an embedded newline acts as Enter
// on the single-line "Type something" field, and a tab/ESC moves focus or dismisses the
// modal. Either way the row the rest of the sequence assumes is gone and the trailing
// keys land on another tab / the review page / the composer, which is why a multi-line
// answer behaved differently every time. Whitespace around the fold is absorbed so
// "a\n\nb" types as "a b". Control characters are folded too, since none of them can
// be *typed* into the field — they can only drive the TUI. The managed path
// (buildRespondAnswers) answers structurally and keeps the text as written.
const flatAt = (freeText: string[], qi: number) =>
  trimAt(freeText, qi)
    .replace(/\s*[\u0000-\u001f\u007f\u2028\u2029]+\s*/g, " ")
    .trim();

const downs = (n: number): KeyStep[] => Array.from({ length: Math.max(0, n) }, () => ({ k: "Down" }));

// A single-select single question answered by clicking option `oi`: move Down to that
// row and Enter (selects and submits). Shared by the claude modal and the menus.
export function buildSinglePickKeys(oi: number): string[] {
  return [...Array(oi).fill("Down"), "Enter"];
}

// claude's AskUserQuestion: a tabbed modal whose free-text row IS an input field —
// you type straight into it, no Enter to enter the row first.
export function buildClaudeSeq(qs: QKQuestion[], sel: string[][], freeText: string[]): KeyStep[] {
  const seq: KeyStep[] = [];
  qs.forEach((q, qi) => {
    const opts = q.options || [];
    const ft = flatAt(freeText, qi);
    const idx = pickedIdx(opts, sel[qi]);
    if (q.multiSelect) {
      // Toggle each checked option in place (Enter toggles, cursor stays). Then, if a
      // custom answer was typed, drop to the "Type something" row and type it — checked
      // options and the custom entry COMBINE (verified in the terminal). Crucially, do
      // NOT press Enter after typing: on a multi-select row Enter toggles the auto-checked
      // custom entry back OFF, silently losing it (the bug). Instead one Down exits the
      // field to the Submit row, and the trailing Enter below submits.
      let cur = 0;
      for (const ci of idx) {
        seq.push(...downs(ci - cur));
        seq.push({ k: "Enter" }); // toggle in place
        cur = ci;
      }
      if (ft) {
        const typeRow = opts.length; // the "Type something" row sits just after the options
        seq.push(...downs(typeRow - cur));
        seq.push({ t: ft }); // typing auto-checks the custom row (NO Enter — it would uncheck)
        // Right is swallowed by the text field, so advance the manual way: Down to the row
        // just below "Type something" — "Next" on an intermediate question, "Submit" on the
        // last — and Enter to activate it. "Next" moves to the next question tab; "Submit"
        // opens the review page (cursor on "Submit answers") where the trailing Enter
        // submits. Verified from the terminal for both single- and multi-question forms.
        seq.push({ k: "Down" }, { k: "Enter" });
      } else {
        seq.push({ k: "Right" }); // advance to the next question / review (Submit) page
      }
    } else if (ft && hasPreview(opts)) {
      // A previewed single-select question drops the "Type something" row entirely —
      // replaced by a per-OPTION "press n to add notes" field — so the old
      // downs(opts.length) lands on the unnumbered "Chat about this" row instead. Typed
      // text is then silently swallowed there (option/menu rows ignore typed text) and
      // the trailing Enter activates "Chat about this", which claude treats as declining
      // the question — the exact "User declined to answer questions" / "(No answer
      // provided)" rejection this was reported as (実測 v2.1.232, docs/build/92 §6).
      // The fix: the cursor always starts a question's tab on option 0, so 'n' opens
      // notes there with no navigation needed; typing + Enter submits with no option
      // picked ("(no option selected) notes: …" — claude's own free-text equivalent
      // for this layout).
      //
      // 'n' rides as a {t} step, not a {k} one: the Agent's /input validates every {k}
      // against a NAMED-key whitelist (allowedKey in session_io.go — Up/Down/…/Enter),
      // and a step it doesn't know rejects the WHOLE request with 400 bad_key, so not a
      // single keystroke reaches the pane and the card just sits there (the reported
      // "自由入力してボタンを押しても無反応"). For a printable character the two are the
      // same byte on the pane anyway — `send-keys n` and `send-keys -l n` both deliver
      // 0x6e — and keeping it client-side means this works against an older Agent too.
      // The step stays separate from the text so the 90ms pacing lets the notes field
      // open before it is typed into.
      seq.push({ t: "n" }, { t: ft }, { k: "Enter" });
    } else if (ft) {
      // single-select free text: move to the "Type something" row, type, then Enter
      // confirms + auto-advances (single-select Enter does NOT toggle-off — verified).
      seq.push(...downs(opts.length));
      seq.push({ t: ft }, { k: "Enter" });
    } else {
      seq.push(...downs(idx[0] ?? 0));
      seq.push({ k: "Enter" }); // select + auto-advance to the next tab
    }
  });
  // A single-select single question has no review page — its own Enter above already
  // submitted (verified: option pick, plain free text, and the preview/notes free text
  // all submit directly). Multi-select reaches "Submit answers" via Right even for one
  // question (its Enter only toggles, never submits), and any multi-question form always
  // ends on the review page, so both of those still need this trailing Enter.
  if (qs.length > 1 || qs[0]?.multiSelect) {
    seq.push({ k: "Enter" }); // Review page: "Submit answers"
  }
  return seq;
}

// What the card actually sends for claude when the submit button is pressed: named keys
// for the one form that has a shorter verified contract, otherwise the full modal walk.
//
// A single-select single question answered with an OPTION is that form. It is the
// sequence the card used to send on the click itself (Down×i, Enter — that Enter selects
// AND submits, there is no review page left to confirm), and deferring the send to a
// button must change only WHEN the keys go out, not which. Routing it through
// buildClaudeSeq instead would append the review page's trailing Enter, which on this
// form lands after the modal is already gone — i.e. in the composer.
export function buildClaudeSubmit(
  qs: QKQuestion[],
  sel: string[][],
  freeText: string[],
): { keys?: string[]; seq?: KeyStep[] } {
  const only = qs.length === 1 && !qs[0]?.multiSelect ? qs[0] : null;
  if (only && !trimAt(freeText, 0)) {
    const idx = pickedIdx(only.options || [], sel[0]);
    if (idx.length) return { keys: buildSinglePickKeys(idx[0]) };
  }
  return { seq: buildClaudeSeq(qs, sel, freeText) };
}

// The simple option menu (codex / opencode / agy): one choice per page, answered by
// Down×i + Enter — which submits the page, advances to the next question and resets
// the cursor to the top, so pages simply concatenate. The trailing Enter completes
// the form (no review page, unlike claude).
//
// writeIn (agy only): the menu appends a "Write-in..." row just after the options.
// Unlike claude's row it must be ENTERED before it accepts text — Enter opens the
// "Your answer:" field and the trailing Enter submits it (agy 1.1.4 実測). Typing
// without that first Enter lands on a plain option row, where the text is dropped and
// Enter picks the highlighted option instead. Left false for codex/opencode, whose
// menus have no verified write-in row.
export function buildMenuSeq(
  qs: QKQuestion[],
  sel: string[][],
  freeText: string[],
  writeIn = false,
): KeyStep[] {
  const seq: KeyStep[] = [];
  qs.forEach((q, qi) => {
    const opts = q.options || [];
    const ft = writeIn ? flatAt(freeText, qi) : "";
    if (ft) {
      seq.push(...downs(opts.length));
      seq.push({ k: "Enter" }, { t: ft }, { k: "Enter" });
      return;
    }
    const ci = Math.max(0, opts.findIndex((o) => o.label === (sel[qi] || [])[0]));
    seq.push(...downs(ci));
    seq.push({ k: "Enter" });
  });
  return seq;
}

// Managed (semantic) sessions answer the pending Interaction structurally — no modal,
// no key encoding. One entry per question, in order; empty when that question was left
// unanswered (a multi-select may legitimately select nothing).
export function buildRespondAnswers(
  qs: QKQuestion[],
  sel: string[][],
  freeText: string[],
): InteractionAnswer[] {
  return qs.map((q, qi) => {
    const idx = pickedIdx(q.options || [], sel[qi]);
    const ft = trimAt(freeText, qi);
    const a: InteractionAnswer = {};
    if (idx.length) a.options = idx;
    if (ft) a.text = ft;
    return a;
  });
}
