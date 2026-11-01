import type { Part, Question, Turn } from "../transcript/types.ts";

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
// Nothing is mutated and `turns` itself is returned when nothing matches. Only the NEWEST
// unanswered match goes (an older identical question that was answered keeps its history),
// and a part that already has an answer or was declined is history, never the pending one.
// Because `turns` stays untouched in the caller's state, the part reappears — decided — on the
// render after the pending payload clears.
export function dropPendingQuestionPart(turns: Turn[], pending: Question[] | null): Turn[] {
  if (!pending || pending.length === 0) return turns;
  let ti = -1;
  let pi = -1;
  turns.forEach((t, i) =>
    (t.parts || []).forEach((p: Part, j) => {
      if (p.kind === "question" && p.questions && sameQuestions(p.questions, pending)) {
        ti = i;
        pi = j;
      }
    }),
  );
  if (ti < 0) return turns;
  const hit = turns[ti].parts![pi];
  if (hit.answer || hit.declined) return turns;
  const parts = turns[ti].parts!.filter((_, j) => j !== pi);
  const next = turns.slice();
  if (parts.length === 0 && !turns[ti].text) next.splice(ti, 1);
  else next[ti] = { ...turns[ti], parts };
  return next;
}
