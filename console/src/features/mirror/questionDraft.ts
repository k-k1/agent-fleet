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
// The pending card and the carried one (docs/log/75) keep their drafts under a key each, but
// hand the draft over when they are two renderings of the same unanswered question — see
// siblingDraftKey.
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

const PENDING_PREFIX = "af.auq-draft.";
const CARRIED_PREFIX = "af.auq-carried-draft.";

/** The live pending card of a session. */
export const questionDraftKey = (session: string | null | undefined): string | null =>
  session ? PENDING_PREFIX + session : null;

/** The carried interaction of a session (docs/log/75) — a different card answering a
 *  different way, so it gets its own key rather than fighting over the pending one. */
export const carriedDraftKey = (session: string | null | undefined): string | null =>
  session ? CARRIED_PREFIX + session : null;

/** The other card of the SAME session: pending ↔ carried.
 *
 *  The two keys stay separate because the cards answer differently (keys vs prose), but the
 *  half-written answer is the user's, not the card's. A session that stops while its
 *  AskUserQuestion is up takes the pending card down and puts the carried one in its place
 *  (and a resume that makes the agent ask again does the reverse), and with a key each, what
 *  had been typed into the free-text row was on screen one moment and gone the next — the
 *  session's stopping is exactly when the user is away from the keyboard and has typed the
 *  most. So a card with no draft of its own falls back to the sibling's, gated on the same
 *  form signature as everything else here: it is restored only onto the identical question
 *  set, i.e. only when it is literally an answer to what is being asked. */
export function siblingDraftKey(key: string | null): string | null {
  if (!key) return null;
  if (key.startsWith(CARRIED_PREFIX)) return PENDING_PREFIX + key.slice(CARRIED_PREFIX.length);
  if (key.startsWith(PENDING_PREFIX)) return CARRIED_PREFIX + key.slice(PENDING_PREFIX.length);
  return null;
}

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

// One key's entry, only when it was stored for exactly this form.
function readAt(key: string | null, qs: Question[], sig: string): QuestionDraft | null {
  if (!key) return null;
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const d = JSON.parse(raw) as { sig?: string; sel?: unknown; freeText?: unknown };
    if (!d || d.sig !== sig) return null;
    return sanitize(qs, d.sel, d.freeText);
  } catch {
    return null;
  }
}

/** readQuestionDraft returns the stored draft for exactly this form, or null — this card's
 *  own, falling back to the sibling card's (siblingDraftKey) when the question set is the
 *  same one. */
export function readQuestionDraft(key: string | null, qs: Question[]): QuestionDraft | null {
  if (!key || !qs?.length) return null;
  const sig = questionSig(qs);
  return readAt(key, qs, sig) ?? readAt(siblingDraftKey(key), qs, sig);
}

// The card on screen owns the draft of the form it is showing, so writing takes it over from
// the sibling key. Without this, emptying the free-text row would only remove this card's
// entry and the next read would fall back to the copy the other card left — the text the user
// just deleted coming back on the next tab switch.
// Only an entry stored for the SAME form is dropped: an unrelated draft (the other card is
// showing a different question) is none of this card's business.
function dropSiblingDraft(key: string | null, sig: string): void {
  const sib = siblingDraftKey(key);
  if (!sib) return;
  try {
    const raw = localStorage.getItem(sib);
    if (!raw) return;
    const d = JSON.parse(raw) as { sig?: string };
    if (d?.sig === sig) localStorage.removeItem(sib);
  } catch {
    /* ignore */
  }
}

/** writeQuestionDraft saves the card's current state; an empty card removes the entry
 *  instead of storing a placeholder, so a session that never half-answered anything
 *  leaves nothing behind. */
export function writeQuestionDraft(key: string | null, sig: string, sel: string[][], freeText: string[]): void {
  if (!key) return;
  const filled = sel.some((a) => a?.length) || freeText.some((t) => (t || "").trim() !== "");
  try {
    dropSiblingDraft(key, sig);
    if (!filled) {
      localStorage.removeItem(key);
      return;
    }
    localStorage.setItem(key, JSON.stringify({ sig, sel, freeText }));
  } catch {
    /* storage unavailable (private mode) — the draft just won't persist */
  }
}

/** clearQuestionDraft drops the card's draft. With `sig` (the form it was showing) the
 *  sibling card's copy of that same form goes too: an answered or cancelled question must
 *  not come back pre-filled through the fallback in readQuestionDraft. */
export function clearQuestionDraft(key: string | null, sig?: string): void {
  if (!key) return;
  try {
    if (sig !== undefined) dropSiblingDraft(key, sig);
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
    clear: () => clearQuestionDraft(key, questionSig(qsRef.current)),
    save: () => writeQuestionDraft(key, questionSig(qsRef.current), stateRef.current.sel, stateRef.current.freeText),
  };
}
