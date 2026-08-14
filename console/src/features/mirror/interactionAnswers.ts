type AnswerPart = {
  kind?: string;
  qid?: string;
  answer?: string;
  declined?: boolean;
  output?: string;
  status?: string;
};

type AnswerTurn = {
  parts?: AnswerPart[];
};

// One interaction tool's resolved tool_result, as the Agent sends it (claude.
// InteractionAnswer): the text, plus whether it was a decline (an Escape out of the
// AskUserQuestion modal) rather than a genuine answer. declined is only meaningful for
// kind=question — ExitPlanMode/delegation entries never set it.
export type InteractionAnswerWire = { text: string; declined?: boolean };

// Reconcile the whole-transcript interaction map onto turns already held by the
// Console. The map is authoritative, so it also repairs a stale window-local answer.
export function patchAnswers<T extends AnswerTurn>(
  turns: T[],
  answers: Record<string, InteractionAnswerWire> | null | undefined,
): T[] {
  if (!answers) return turns;
  let changed = false;
  const next = turns.map((t) => {
    if (!t.parts) return t;
    let pchanged = false;
    const parts = t.parts.map((p) => {
      const a = p.qid ? answers[p.qid] : undefined;
      if (!a || !a.text) return p;
      if (p.kind === "question" && (p.answer !== a.text || !!p.declined !== !!a.declined)) {
        pchanged = true;
        return { ...p, answer: a.text, declined: a.declined };
      }
      if (p.kind === "plan" && p.answer !== a.text) {
        pchanged = true;
        return { ...p, answer: a.text };
      }
      if (p.kind === "delegation" && p.status !== "completed") {
        pchanged = true;
        return { ...p, output: a.text, status: "completed" };
      }
      return p;
    });
    if (!pchanged) return t;
    changed = true;
    return { ...t, parts };
  });
  return changed ? next : turns;
}
