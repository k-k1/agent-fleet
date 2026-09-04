// PendingQuestions — the interactive form for the currently-awaiting AskUserQuestion.
//
// Owner-only, and deliberately NOT part of transcript/: answering means driving the
// agent's TUI modal (or posting a structured Interaction response), which only the
// session's owner can do. The transcript layer renders the *answered* form of a question
// (transcript/blocks.tsx QuestionBlock); this is the live one.
//
// EVERY form works the same way: clicking an option only SELECTS it, and the answer
// leaves the card when the submit button is pressed. A single-question card used to
// send on the click itself, which made a misclick unrecoverable — there was no state
// between "reading the options" and "answered", so a slip of the finger committed an
// answer the user never got to look at, and the preview of the option they were
// comparing against was already gone.
// Answers are sent one page at a time (multi-select choices joined) so the terminal
// modal advances through each question and doesn't close after the first pick.
// Every path is key-driven; NEVER send an option label as text — the modal ignores
// typed text on option rows and the Enter confirms the highlighted first option
// (measured on v2.1.204, docs/build/92-driving-a-tui.md).

import { useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { t as tr } from "../../lib/i18n/index.ts";
import type { InteractionAnswer } from "../../core/api/client.ts";
import { buildClaudeSubmit, buildMenuSeq, buildRespondAnswers } from "./questionKeys.ts";
import { OptionBody, hasPreview } from "./transcript/blocks.tsx";
import type { Question } from "./transcript/types.ts";

export function PendingQuestions({
  questions,
  onSubmitKeys,
  onSubmitSeq,
  onRespond,
  onCancel,
  onSubmitAnswers,
  submitLabel,
  cancelLabel,
  sending,
  answerMode = "claude",
  multiPage = false,
  writeIn = false,
}: {
  questions: Question[];
  onSubmitKeys: (keys: string[]) => void;
  onSubmitSeq: (seq: Array<{ k?: string; t?: string }>) => void;
  // onRespond (managed sessions): structured answer to the pending Interaction (docs/log/27
  // §5 — one entry per question, in order). When given, every path answers semantically and
  // none of the TUI key driving below runs.
  onRespond?: (answers: InteractionAnswer[]) => void;
  // onCancel: dismiss the pending question without answering (Escape / Interrupt) so the
  // conversation can continue with a fresh prompt instead of an answer.
  onCancel?: () => void;
  // onSubmitAnswers (carried interactions, docs/log/75): the answer to a question whose modal
  // no longer exists. When given it takes precedence over every other submit path and builds
  // no keys or seq at all — a key with nothing to aim at would decide something else if it
  // landed in a live pane. The Agent delivers the answer as prose after resuming. The option
  // UI (multi-select, free text, preview comparison) is reused unchanged.
  onSubmitAnswers?: (answers: Array<{ labels: string[]; notes: string }>) => void;
  // The buttons mean something different for a carried interaction (send answer → answer and
  // resume, cancel → discard), so the labels are overridable.
  submitLabel?: string;
  cancelLabel?: string;
  sending: boolean;
  // "claude": AskUserQuestion's tabbed modal (free-text single / Down-Enter-Right multi).
  // "menu": codex/opencode ask via a simple option menu — a single-select question is
  // answered by moving Down to the option index and pressing Enter.
  answerMode?: "claude" | "menu";
  // multiPage (codex): the menu pages through a multi-question form one question at a
  // time — each page is answered by Down×i + Enter, which submits the page, advances
  // to the next and resets the cursor to the top (verified on codex 0.144.3). So a
  // multi-question menu IS drivable: build a selection here, then send the pages'
  // sequences in one go. opencode's dialog has no verified paging keys, so it stays
  // single-question-only (multiPage=false → terminal hint for multi).
  multiPage?: boolean;
  // writeIn (agy): the menu appends a "Write-in..." row just after the options, so a
  // menu question can ALSO be answered with free text — without this the card showed
  // only the listed options and the 4th row was unreachable from chat. The row is
  // entered rather than typed into directly: Down×options.length, Enter (opens
  // "Your answer:"), the text, then Enter submits — measured on agy 1.1.4. That extra Enter
  // is why this is its own flag and not folded into claude's free-text path, whose row
  // IS the field (type straight into it).
  writeIn?: boolean;
}) {
  const qs = questions || [];
  const [sel, setSel] = useState<string[][]>(() => qs.map(() => []));
  // Per-question free-text ("Type something"). Filled → that question is answered by
  // free text instead of an option (mutually exclusive with a selection, below).
  const [freeText, setFreeText] = useState<string[]>(() => qs.map(() => ""));
  const single = qs.length === 1 && !qs[0]?.multiSelect;
  const menu = answerMode === "menu";
  const semantic = !!onRespond;
  // What a menu can drive: a single-select single question always; a multi-question
  // form only when the dialog pages (multiPage) and no question is multi-select (the
  // codex dialog is one choice per page). Anything else is shown read-only with a
  // hint to answer in the terminal. Semantic (managed) answers aren't bound by the
  // TUI modal's key behavior, so every form is drivable.
  const menuDrivable =
    semantic ||
    !!onSubmitAnswers || // a carried interaction fires no keys, so the modal's limits do not apply
    (menu && (single || (multiPage && qs.length > 1 && qs.every((q) => !q.multiSelect))));

  const clearFree = (qi: number) =>
    setFreeText((prev) => (prev[qi] ? prev.map((v, i) => (i === qi ? "" : v)) : prev));

  const toggle = (qi: number, label: string, multi?: boolean) => {
    // Multi-select COMBINES a custom "Type something" entry with checked options (verified
    // in the terminal), so only a single-select pick is mutually exclusive with free text.
    if (!multi) clearFree(qi);
    setSel((prev) => {
      const next = prev.map((a) => a.slice());
      const cur = next[qi] || [];
      if (multi) next[qi] = cur.includes(label) ? cur.filter((x) => x !== label) : [...cur, label];
      else next[qi] = cur[0] === label ? [] : [label];
      return next;
    });
  };

  const setFree = (qi: number, v: string, multi?: boolean) => {
    setFreeText((prev) => prev.map((x, i) => (i === qi ? v : x)));
    // Single-select: typing a custom answer drops the radio pick (mutually exclusive).
    // Multi-select: keep the checked options — the custom entry is an ADDITIONAL choice.
    if (v && !multi) setSel((prev) => ((prev[qi] || []).length ? prev.map((a, i) => (i === qi ? [] : a)) : prev));
  };

  // A question is answered by a selection OR free text (multi-select may be left empty).
  const canSubmit = qs.every(
    (q, qi) => (freeText[qi] || "").trim() !== "" || q.multiSelect || (sel[qi] || []).length > 0,
  );

  // Drive the modal with named keys, matching the real AskUserQuestion behavior
  // (verified against the terminal). Each question page starts with the cursor at the
  // top option; ↑/↓ navigate options, ←/→ switch question tabs, Enter selects/toggles.
  //   single-select: move Down to the choice, Enter — this selects AND auto-advances
  //                  to the next tab.
  //   multi-select:  Enter TOGGLES in place (cursor stays); after toggling every
  //                  choice, Right advances to the next tab.
  // After all questions we land on the Submit tab (Review page); a final Enter
  // activates "Submit answers".
  // Drive codex's paged menu: per question Down×i to the picked option, then Enter —
  // submits the page, auto-advances to the next question and resets the cursor to the
  // top, so the pages' sequences simply concatenate. The trailing page's Enter
  // completes the whole form (no review page, unlike claude's modal).
  // Semantic submit (managed): translate the built selection / free text into
  // structured per-question answers — no TUI key encoding, no modal quirks.
  const submitRespond = () => onRespond!(buildRespondAnswers(qs, sel, freeText));

  // Carried interaction (docs/log/75): pass the selection and free text through as-is, with
  // no conversion to a key sequence.
  const submitCarried = () =>
    onSubmitAnswers!(qs.map((_, qi) => ({ labels: sel[qi] || [], notes: (freeText[qi] || "").trim() })));

  const submitMenu = () => {
    if (onSubmitAnswers) return submitCarried();
    if (semantic) return submitRespond();
    onSubmitSeq(buildMenuSeq(qs, sel, freeText, writeIn));
  };

  const submit = () => {
    if (onSubmitAnswers) return submitCarried();
    if (semantic) return submitRespond();
    // Which keys a built selection becomes is the modal's contract, so it lives in
    // questionKeys (and is pinned by its tests); the card only routes the result.
    const out = buildClaudeSubmit(qs, sel, freeText);
    if (out.keys) return onSubmitKeys(out.keys);
    onSubmitSeq(out.seq!);
  };

  const wide = hasPreview(qs);
  return (
    <div className="mt-question">
      {qs.map((qn, qi) => (
        <div className="mq" key={qi}>
          <div className="mq-head">
            <Icon name="comment-discussion" />
            {qn.header && <span className="mq-header">{qn.header}</span>}
            {qs.length > 1 && (
              <span className="mq-page muted">
                {qi + 1}/{qs.length}
              </span>
            )}
            {qn.multiSelect && <span className="mq-multi muted">{tr("mirror.multi_select")}</span>}
          </div>
          {qn.question && <div className="mq-text">{qn.question}</div>}
          <div className={"mq-options" + (wide ? " wide" : "")}>
            {(qn.options || []).map((o, oi) => {
              const checked = (sel[qi] || []).includes(o.label);
              // Selecting is all a click does — on every form, including the single
              // question that used to answer on the spot. Clicking the picked option
              // again clears it (toggle), so a misclick is undone in the card instead of
              // needing the turn interrupted.
              const pick = () => toggle(qi, o.label, qn.multiSelect);
              return (
                <button
                  type="button"
                  className={"mq-opt" + (checked ? " checked" : "")}
                  key={oi}
                  disabled={sending || (menu && !menuDrivable)}
                  onClick={pick}
                  title={o.description || o.label}
                >
                  {/* The marker is shown on every form now: with the send deferred, what
                      is currently picked is state the user has to be able to SEE. */}
                  <span className="mq-mark">{qn.multiSelect ? (checked ? "☑" : "☐") : checked ? "◉" : "○"}</span>
                  <OptionBody o={o} />
                </button>
              );
            })}
          </div>
          {(!menu || writeIn) && (
            // Free-text ("Type something" / agy's "Write-in...") — rendered for single
            // questions too: the
            // composer can't answer an AUQ (typed text is ignored and Enter confirms
            // option 1), so this in-card row, driven via submit()'s reliable
            // Down-to-the-row-then-type sequence, is the only working free-text path.
            // Typing here clears a single-select pick — the two are mutually exclusive.
            <textarea
              className="mq-freetext"
              rows={2}
              placeholder={tr("mirror.freeform_ph")}
              value={freeText[qi] || ""}
              disabled={sending}
              onChange={(e) => setFree(qi, e.target.value, qn.multiSelect)}
            />
          )}
        </div>
      ))}
      <div className="mq-submit-row mq-footer">
        {onCancel && (
          // Cancel the question without answering — dismiss the AUQ (Escape for TUI,
          // Interrupt for managed) so the user can steer into a normal discussion instead
          // of being forced to pick an option first. Always available, even for forms we
          // can't drive from chat (the terminal-hint case).
          <button
            type="button"
            className="ghost mq-cancel"
            disabled={sending}
            title={tr("mirror.question_cancel_title")}
            onClick={onCancel}
          >
            <Icon name="close" /> {cancelLabel || tr("mirror.question_cancel")}
          </button>
        )}
        {!menu && (
          // The only way an answer leaves the card: enabled once every question has a pick
          // or free text (canSubmit), which for a single question means one option chosen.
          <button
            type="button"
            className="btn primary mq-submit"
            disabled={sending || !canSubmit}
            onClick={submit}
          >
            {submitLabel || tr("mirror.submit_answer")}
          </button>
        )}
        {menu && menuDrivable && (
          // The menus (codex/opencode/agy) submit the same way: pick per question above,
          // then send every page's key sequence at once. A single-question menu now needs
          // this button too — its options no longer send on click.
          <button
            type="button"
            className="btn primary mq-submit"
            disabled={sending || !canSubmit}
            onClick={submitMenu}
          >
            {submitLabel || tr("mirror.submit_answer")}
          </button>
        )}
        {menu && !menuDrivable && (
          // A multi-select menu (or a multi-question one on a dialog whose paging keys
          // aren't verified) we can't reliably drive from chat.
          <span className="muted mq-terminal-hint">{tr("mirror.question_terminal")}</span>
        )}
      </div>
    </div>
  );
}
