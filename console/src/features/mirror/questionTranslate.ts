// mirror/questionTranslate — the translate button on a pending question card (AskUserQuestion).
//
// An agent asks in whatever language its turn happened to drift into, and the card is where a
// reader most needs to understand every word: the answer they pick is sent back as-is. The card
// reuses the per-answer translation (useTranslate, docs/log/97) instead of a wiring of its own,
// so the cache, the prefetch on open, the ledger and the Settings switch all apply unchanged.
//
// The whole card travels as ONE request part. The Agent runs one CLI call per part and refuses
// more than 8 parts (session_translate.go translateMaxParts), so a part per label would both
// cost a run per option and break on any card with more than a handful of them. Fields are
// tagged with inline-code markers because the translation prompt keeps inline code verbatim;
// the reply is split back on those markers, and a field whose marker did not survive simply
// shows its original text.
//
// Translation is display-only. The options' labels are what the answer is built from (key
// driving, managed answers, carried answers all match on the label), so the card keeps
// selecting and sending the ORIGINAL label whatever is on screen.

import { useEffect, useRef } from "react";
import { looksForeign, turnTranslateKey } from "./translate.ts";
import type { TranscriptTranslateWiring } from "./useTranslate.ts";
import type { Question } from "./transcript/types.ts";

type Field = "header" | "text" | "label" | "desc";

const marker = (qi: number, field: Field, oi?: number): string =>
  oi === undefined ? `Q${qi + 1}.${field}` : `Q${qi + 1}.O${oi + 1}.${field}`;

/** The card as one Markdown text: one marked paragraph per non-empty field. "" when the card
 *  has nothing to translate. Previews are left out: they are mockups and code, which the
 *  translation must not touch anyway. */
export function questionTranslateSource(qs: Question[]): string {
  const out: string[] = [];
  const push = (m: string, v?: string) => {
    if (v && v.trim()) out.push("`" + m + "` " + v.trim());
  };
  qs.forEach((q, qi) => {
    push(marker(qi, "header"), q.header);
    push(marker(qi, "text"), q.question);
    (q.options || []).forEach((o, oi) => {
      push(marker(qi, "label", oi), o.label);
      push(marker(qi, "desc", oi), o.description);
    });
  });
  return out.join("\n\n");
}

const MARKER_RE = /^[ \t]*`(Q\d+\.(?:header|text|O\d+\.(?:label|desc)))`[ \t]*/gm;

/** The translated fields by marker. Text before the first marker (a preamble the model was told
 *  not to write) is dropped. */
export function parseQuestionTranslation(translated: string): Map<string, string> {
  const found: Array<{ m: string; start: number; end: number }> = [];
  for (const hit of translated.matchAll(MARKER_RE)) {
    found.push({ m: hit[1], start: hit.index!, end: hit.index! + hit[0].length });
  }
  const out = new Map<string, string>();
  found.forEach((f, i) => {
    const v = translated.slice(f.end, i + 1 < found.length ? found[i + 1].start : undefined).trim();
    if (v && !out.has(f.m)) out.set(f.m, v);
  });
  return out;
}

/** The questions as they are displayed with the translation on, index for index with the
 *  originals. Labels are translated for the eye only: PendingQuestions picks and sends the
 *  original option at the same index. */
export function translatedQuestions(qs: Question[], fields: Map<string, string>): Question[] {
  return qs.map((q, qi) => ({
    ...q,
    header: q.header ? (fields.get(marker(qi, "header")) ?? q.header) : q.header,
    question: q.question ? (fields.get(marker(qi, "text")) ?? q.question) : q.question,
    options: (q.options || []).map((o, oi) => ({
      ...o,
      label: fields.get(marker(qi, "label", oi)) ?? o.label,
      description: o.description ? (fields.get(marker(qi, "desc", oi)) ?? o.description) : o.description,
    })),
  }));
}

/** What the card needs to render the button and the translated view. */
export interface QuestionTranslateView {
  /** The questions to display; the original ones unless the translation is shown. */
  questions: Question[];
  /** The prose above the card, translated when shown. */
  lead: string;
  shown: boolean;
  busy: boolean;
  error?: string;
  toggle: () => void;
}

/**
 * The card's translate wiring, or undefined when no button is offered: the feature is off, or
 * the card already reads in the reader's language (the same looksForeign test as an answer).
 * `lead` is the prose shown above the questions (the managed session's pendingText, a carried
 * interaction's text); it goes out as a second part and is displayed like a turn's prose.
 * `autoEligible` is the transcript's "arrived while the reader was watching" (TranscriptView
 * arrivedAfter): without it, opening a long session with automatic translation on would press
 * for every question in its history.
 */
export function useQuestionTranslate(
  tx: TranscriptTranslateWiring | undefined,
  qs: Question[],
  lead = "",
  autoEligible = true,
): QuestionTranslateView | undefined {
  const source = tx ? questionTranslateSource(qs) : "";
  const texts = [lead.trim() ? lead : "", source].filter((s) => s !== "");
  const key = tx ? turnTranslateKey(texts) : "";
  const shown = !!key && !!tx?.shown(key);
  const offered =
    !!tx && !!key && !!source && (shown || !!tx.get(source) || looksForeign(texts.join("\n\n"), tx.lang));

  // Settings > AI assistance "translate automatically" is the reader's standing press. A pending
  // card is by definition a finished turn waiting for them, so it qualifies on sight; useTranslate's
  // autoPress latches per key, so the second-by-second re-render cannot fire it twice.
  const auto = offered && autoEligible && !!tx?.auto && !shown;
  const args = useRef({ key, texts });
  args.current = { key, texts };
  useEffect(() => {
    if (auto) tx?.autoPress(args.current.key, args.current.texts);
  }, [auto, key, tx]);

  if (!offered || !tx) return undefined;
  const got = shown ? tx.get(source) : undefined;
  const leadTx = shown && lead.trim() ? tx.get(lead) : undefined;
  return {
    questions: got ? translatedQuestions(qs, parseQuestionTranslation(got)) : qs,
    lead: leadTx ?? lead,
    shown,
    busy: tx.busy(key),
    error: tx.error(key),
    toggle: () => tx.toggle(key, texts),
  };
}
