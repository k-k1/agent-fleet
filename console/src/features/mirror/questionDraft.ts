// Draft persistence for an UNANSWERED question card (localStorage), the companion of
// lib/draft.ts for the composer.
//
// A pending AskUserQuestion is answered by a deliberate second step — clicking an option
// only selects it, the answer leaves the card on the submit button (PendingQuestions).
// Everything between those two steps lived in component state, and the card's component
// dies whenever the view does: selecting another tab of the pane unmounts the whole
// MirrorView (only the selected tab is mounted), as does toggling to the terminal. So a
// user who picked an option, then went to look at a file or the diff to decide, came back
// to an empty card — on a multi-question form, several answers' worth of reading thrown
// away. Persisting the selection is what makes "go and check, then answer" possible at all.
//
// The draft is gated on a signature of the FORM, never on the session alone: a stored
// selection may only ever be restored onto the identical question set. A card whose
// questions changed (the next AUQ of the same session, a managed question re-asked under a
// new interaction id) starts empty rather than pre-filled with an answer to something else,
// which the user could submit without reading it.
//
// Storage errors are swallowed everywhere (private mode, quota): the draft just doesn't
// persist, exactly as before this file existed.

import { useEffect, useRef, useState } from "react";
import type { Dispatch, SetStateAction } from "react";
import type { Question } from "./transcript/types.ts";

/** What the card holds between reading the question and submitting: the labels picked per
 *  question (single-select holds at most one) and the per-question free text. */
export interface QuestionDraft {
  sel: string[][];
  freeText: string[];
}

/** The live pending card of a session. */
export const questionDraftKey = (session: string | null | undefined): string | null =>
  session ? "af.auq-draft." + session : null;

/** The carried interaction of a session (docs/log/75) — a different card answering a
 *  different way, so it gets its own key rather than fighting over the pending one. */
export const carriedDraftKey = (session: string | null | undefined): string | null =>
  session ? "af.auq-carried-draft." + session : null;

// The form's identity. Only what the user answers WITH is in it (question text, the
// offered labels, whether it is multi-select, and the managed interaction id): a redrawn
// card whose descriptions or previews were reworded is still the same question, and losing
// the draft to that would be the bug this file exists to fix.
export function questionSig(qs: Question[]): string {
  return JSON.stringify(
    (qs || []).map((q) => [q.id || "", q.question || "", q.multiSelect ? 1 : 0, (q.options || []).map((o) => o.label)]),
  );
}

// Restored labels are filtered against the options actually offered and the arrays are
// re-shaped to the current form. The signature already guarantees both, so this is
// defense in depth against a hand-edited or half-written entry: a label with no row would
// otherwise reach the key builders, and "answered" would be a selection nothing on screen
// shows (buildClaudeSubmit drops unknown labels, so the card would submit an empty answer).
function sanitize(qs: Question[], sel: unknown, freeText: unknown): QuestionDraft {
  const selArr = Array.isArray(sel) ? sel : [];
  const txtArr = Array.isArray(freeText) ? freeText : [];
  return {
    sel: qs.map((q, qi) => {
      const labels = (q.options || []).map((o) => o.label);
      const got = Array.isArray(selArr[qi]) ? (selArr[qi] as unknown[]) : [];
      const kept = got.filter((l): l is string => typeof l === "string" && labels.includes(l));
      return q.multiSelect ? kept : kept.slice(0, 1);
    }),
    freeText: qs.map((_, qi) => (typeof txtArr[qi] === "string" ? (txtArr[qi] as string) : "")),
  };
}

/** readQuestionDraft returns the stored draft for exactly this form, or null. */
export function readQuestionDraft(key: string | null, qs: Question[]): QuestionDraft | null {
  if (!key || !qs?.length) return null;
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const d = JSON.parse(raw) as { sig?: string; sel?: unknown; freeText?: unknown };
    if (!d || d.sig !== questionSig(qs)) return null;
    return sanitize(qs, d.sel, d.freeText);
  } catch {
    return null;
  }
}

/** writeQuestionDraft saves the card's current state; an empty card removes the entry
 *  instead of storing a placeholder, so a session that never half-answered anything
 *  leaves nothing behind. */
export function writeQuestionDraft(key: string | null, sig: string, sel: string[][], freeText: string[]): void {
  if (!key) return;
  const filled = sel.some((a) => a?.length) || freeText.some((t) => (t || "").trim() !== "");
  try {
    if (!filled) {
      localStorage.removeItem(key);
      return;
    }
    localStorage.setItem(key, JSON.stringify({ sig, sel, freeText }));
  } catch {
    /* storage unavailable (private mode) — the draft just won't persist */
  }
}

export function clearQuestionDraft(key: string | null): void {
  if (!key) return;
  try {
    localStorage.removeItem(key);
  } catch {
    /* ignore */
  }
}

/** useQuestionDraft is the card's (selection, free text) state, backed by localStorage:
 *  restored on mount, written through on every edit, and reset when the form itself
 *  changes under a mounted card (key=null disables persistence entirely).
 *
 *  `clear` is called when the answer goes out and on cancel; `save` puts it back when the
 *  send is refused — the card stays on screen in that case, and a draft cleared for an
 *  answer that never left would be lost the moment the user switched tabs to go and look
 *  at why. */
export function useQuestionDraft(
  key: string | null,
  qs: Question[],
): {
  sel: string[][];
  setSel: Dispatch<SetStateAction<string[][]>>;
  freeText: string[];
  setFreeText: Dispatch<SetStateAction<string[]>>;
  clear: () => void;
  save: () => void;
} {
  const [restored] = useState(() => readQuestionDraft(key, qs));
  const [sel, setSel] = useState<string[][]>(() => restored?.sel ?? qs.map(() => []));
  const [freeText, setFreeText] = useState<string[]>(() => restored?.freeText ?? qs.map(() => ""));

  const sig = questionSig(qs);
  // The effect needs the questions only to re-read a draft after a form change; keeping
  // them out of its dependencies stops every poll's fresh array identity from re-running it.
  const qsRef = useRef(qs);
  qsRef.current = qs;
  const formRef = useRef(key + "|" + sig);
  const stateRef = useRef({ sel, freeText });
  stateRef.current = { sel, freeText };

  useEffect(() => {
    const form = key + "|" + sig;
    if (formRef.current !== form) {
      // The card was handed another form (a new question set arrived without a remount, or
      // the pane switched session). Load THAT form's draft instead of saving this one's
      // state over it.
      formRef.current = form;
      const d = readQuestionDraft(key, qsRef.current);
      setSel(d?.sel ?? qsRef.current.map(() => []));
      setFreeText(d?.freeText ?? qsRef.current.map(() => ""));
      return;
    }
    writeQuestionDraft(key, sig, sel, freeText);
  }, [key, sig, sel, freeText]);

  return {
    sel,
    setSel,
    freeText,
    setFreeText,
    clear: () => clearQuestionDraft(key),
    save: () => writeQuestionDraft(key, questionSig(qsRef.current), stateRef.current.sel, stateRef.current.freeText),
  };
}
