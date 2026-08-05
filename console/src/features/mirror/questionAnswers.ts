// Reading back an ANSWERED AskUserQuestion: which options were picked, and what the
// user typed.
//
// claude reports the answer as one prose tool_result, with nothing escaped:
//   The user answered: "<q1>"="<a1>", "<q2>"="<a2>". Read the answers carefully — …
//   Your questions have been answered: "<q1>"="<a1>". You can now continue …   (older)
// and, when the picked option carried a `preview`, that artifact rides along too:
//   … "<q1>"="<a1>" selected preview:\n<mockup>, "<q2>"="<a2>". …
//
// So a `"` the user typed inside a free-text answer is indistinguishable from the quote
// that closes it, and the obvious /"[^"]*"\s*=\s*"([^"]*)"/ stops at the first typed
// quote: the card showed `A+Bでいこう。ただし` for an answer that went on for three more
// lines after `ただし"二重再開の回避"は…`. The truncation was silent — the mirror looked
// like the user had simply said less than they did.
//
// The question texts are known at render time, so anchor on them instead: an answer runs
// from its own `"<question>"="` up to the NEXT question's anchor (or, for the last one,
// to the closing quote before the trailing sentence). Quotes inside an answer are then
// just characters. The wording of the prose around the pairs is claude's, and it has
// already changed once, so every step degrades instead of failing: no anchors → the old
// pair regex → the raw string.

const escRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

// An option that carried a `preview` (an ASCII mockup, a code snippet) is reported as
//   "<q>"="<label>" selected preview:\n<preview text>
// — the OPTION's own artifact, appended after the closing quote, not anything the user
// wrote. Left in, the answer matched no label at all: every radio drew unpicked and the
// mockup's own quotes truncated it mid-preview, so a picked option read as free text.
// Cut there and the answer is the bare label again.
const cutPreview = (s: string): string | null => {
  const m = /"[ \t]*selected preview:/.exec(s);
  return m ? s.slice(0, m.index).trim() : null;
};

// Strip the quote that closes an answer plus the `, ` before the next pair.
const cutSep = (s: string) => s.replace(/"[\s,、，]*$/, "").trim();

// The last answer is followed by the closing quote and claude's trailing sentence
// ("Read the answers carefully …" / "You can now continue …"). Neither trailer has ever
// contained a quote, so the LAST quote in the chunk is the closing one — and quotes the
// user typed, which all sit before it, survive.
const cutTail = (s: string) => {
  const q = s.lastIndexOf('"');
  return (q >= 0 ? s.slice(0, q) : s).trim();
};

// Anchored split — null when any question text is missing from the result (a reworded
// prompt, a truncated result, or a format we don't know), so the caller can fall back.
function byAnchors(text: string, prompts: string[]): string[] | null {
  const at: { start: number; anchor: number }[] = [];
  let from = 0;
  for (const p of prompts) {
    const q = p.trim();
    if (!q) return null;
    const m = new RegExp('"' + escRe(q) + '"\\s*=\\s*"').exec(text.slice(from));
    if (!m) return null;
    const anchor = from + m.index;
    from = anchor + m[0].length;
    at.push({ start: from, anchor });
  }
  return at.map((cur, i) => {
    const last = i + 1 >= at.length;
    const chunk = last ? text.slice(cur.start) : text.slice(cur.start, at[i + 1].anchor);
    // The preview cut, when it applies, already ends the answer at its closing quote —
    // running cutSep/cutTail after it would only chew into the label.
    return cutPreview(chunk) ?? (last ? cutTail(chunk) : cutSep(chunk));
  });
}

// Parse the tool_result into one answer per question, in order. Returns [] when the text
// can't be split at all — the caller then shows the whole string on every card, which is
// noisy but never lies.
export function parseQuestionAnswers(raw: string, prompts: (string | undefined)[]): string[] {
  const text = (raw || "").trim();
  if (!text || !prompts.length) return [];
  const anchored = byAnchors(
    text,
    prompts.map((p) => p || ""),
  );
  if (anchored) return anchored;
  // Legacy path: pair up on quotes alone. Truncates at a typed `"` (that is the bug this
  // module exists for), but it is the only thing left when the question text doesn't
  // appear in the result verbatim.
  return [...text.matchAll(/"[^"]*"\s*=\s*"([^"]*)"/g)].map((m) => m[1].trim());
}

// One question's answer resolved against its options: the labels that were picked, and
// the free-text ("Type something") entry, which multi-select may COMBINE with picks.
//
// The value is a list of picked labels and/or one free-text entry joined by ", "
// (localized "、"), so splitting on that separator is the only way to tell them apart —
// but a free-text reply is prose that contains commas of its own. Split, therefore, only
// as far as it pays off: an answer that matches an option EXACTLY, or whose segments do,
// is a pick list; anything else is the user's words and is kept verbatim, punctuation
// and all. Matching is by exact segment equality — never substring containment, or
// "AWSは使わない" would check the option "AWS" and then vanish from the card.
export function resolveAnswer(answer: string, labels: string[]): { chosen: string[]; extras: string[] } {
  const a = (answer || "").trim();
  if (!a) return { chosen: [], extras: [] };
  // Whole-answer match first: an option label may itself contain a comma.
  if (labels.includes(a)) return { chosen: [a], extras: [] };
  const segs = a
    .split(/\s*[,、，]\s*/)
    .map((s) => s.trim())
    .filter(Boolean);
  const chosen = segs.filter((s) => labels.includes(s));
  if (!chosen.length) return { chosen: [], extras: [a] };
  return { chosen, extras: segs.filter((s) => !labels.includes(s)) };
}
