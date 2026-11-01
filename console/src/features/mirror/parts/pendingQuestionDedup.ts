import type { Question, Turn } from "../transcript/types.ts";

// sameQuestions compares two AskUserQuestion payloads by what the member reads: header,
// prompt, multiSelect and every option's label / description / preview. Question.id is left out
// on purpose — only a managed Interaction carries one, and the transcript part never does.
function sameQuestions(a: Question[], b: Question[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((qa, i) => {
    const qb = b[i];
    if ((qa.header || "") !== (qb.header || "")) return false;
    if ((qa.question || "") !== (qb.question || "")) return false;
    if (!!qa.multiSelect !== !!qb.multiSelect) return false;
    const oa = qa.options || [];
    const ob = qb.options || [];
    return (
      oa.length === ob.length &&
      oa.every(
        (o, j) =>
          o.label === ob[j].label &&
          (o.description || "") === (ob[j].description || "") &&
          (o.preview || "") === (ob[j].preview || ""),
      )
    );
  });
}

// dropPendingQuestionPart removes the still-open transcript copy of the question whose
// actionable card (PendingQuestions) is on screen, so one question is drawn once.
//
// WHY here as well as in the Agent (hidePendingInteraction): the Agent can only hide the part
// when it is in the same poll window that surfaces the pending payload. claude's jsonl line
// can land before the hook's pending-question file, so poll N delivers the part, poll N+1
// delivers the payload, and the part is then behind the client's cursor for good. The client
// is the only place that sees both. Matching is by content, like the Agent's: the payload has
// no tool_use id.
//
// Content alone cannot prove two parts are the same tool_use (the payload has no id), so the
// match must also be POSITIONAL: the duplicate is the very last part of the last held turn. An
// open AskUserQuestion blocks claude, so nothing is written after its line; an older identical
// question that was left open and then re-asked has later turns behind it, and is history —
// when in doubt the part stays (a double card is the lesser evil than a vanished one).
//
// Nothing is mutated and `turns` itself is returned when nothing matches. A part that already
// has an answer or was declined is history, never the pending one. Because `turns` stays
// untouched in the caller's state, the part reappears — decided — on the render after the
// pending payload clears.
export function dropPendingQuestionPart(turns: Turn[], pending: Question[] | null): Turn[] {
  if (!pending || pending.length === 0) return turns;
  const ti = turns.length - 1;
  const last = ti >= 0 ? turns[ti].parts : undefined;
  if (!last || last.length === 0) return turns;
  const pi = last.length - 1;
  const hit = last[pi];
  if (hit.kind !== "question" || !hit.questions || !sameQuestions(hit.questions, pending)) return turns;
  if (hit.answer || hit.declined) return turns;
  const parts = last.slice(0, pi);
  const next = turns.slice();
  if (parts.length === 0 && !turns[ti].text) next.splice(ti, 1);
  else next[ti] = { ...turns[ti], parts };
  return next;
}
