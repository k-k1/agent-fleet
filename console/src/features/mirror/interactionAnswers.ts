type AnswerPart = {
  kind?: string;
  qid?: string;
  answer?: string;
  output?: string;
  status?: string;
};

type AnswerTurn = {
  parts?: AnswerPart[];
};

// Reconcile the whole-transcript interaction map onto turns already held by the
// Console. The map is authoritative, so it also repairs a stale window-local answer.
export function patchAnswers<T extends AnswerTurn>(
  turns: T[],
  answers: Record<string, string> | null | undefined,
): T[] {
  if (!answers) return turns;
  let changed = false;
  const next = turns.map((t) => {
    if (!t.parts) return t;
    let pchanged = false;
    const parts = t.parts.map((p) => {
      const a = p.qid ? answers[p.qid] : undefined;
      if (!a) return p;
      if ((p.kind === "question" || p.kind === "plan") && p.answer !== a) {
        pchanged = true;
        return { ...p, answer: a };
      }
      if (p.kind === "delegation" && p.status !== "completed") {
        pchanged = true;
        return { ...p, output: a, status: "completed" };
      }
      return p;
    });
    if (!pchanged) return t;
    changed = true;
    return { ...t, parts };
  });
  return changed ? next : turns;
}
