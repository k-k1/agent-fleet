import type { Question, Turn } from "./transcript/types.ts";

// Hides the transcript card that duplicates the still-pending AskUserQuestion.
//
// claude writes the question's tool_use at ASK time, so while the modal is up the same ask is
// both an inert transcript part and the actionable pending card. The Agent strips the part only
// when it sits in the same poll window as the hook payload; when the jsonl line lands first, an
// earlier poll has already delivered it and the cursor is past it (#3190). Whatever the poll
// order, the Console is the one place that knows both, so it de-duplicates here.
//
// Content rule, same as the Agent's hidePendingInteraction: the hook payload carries no
// tool_use id, so parts match by content, and only the LAST unanswered match is hidden — a
// question asked again with identical content leaves the older, decided card in the history,
// and an answered part is never hidden (a stale payload must not erase history).
type Sig = [string, string, boolean, [string, string, string][]][];

function sig(qs: Question[]): string {
  const s: Sig = qs.map((q) => [
    q.header ?? "",
    q.question ?? "",
    !!q.multiSelect,
    (q.options ?? []).map((o) => [o.label ?? "", o.description ?? "", o.preview ?? ""]),
  ]);
  return JSON.stringify(s);
}

export interface PendingDupResult {
  turns: Turn[];
  // A transcript part was hidden: the prose claude wrote just before the question is in the
  // transcript too, so the pending card must not repeat it (pendingText).
  hidden: boolean;
}

export function hidePendingQuestionDup(turns: Turn[], pending: Question[] | null | undefined): PendingDupResult {
  const none = { turns, hidden: false };
  if (!pending || pending.length === 0) return none;
  const want = sig(pending);
  let ti = -1;
  let pi = -1;
  for (let i = 0; i < turns.length; i++) {
    const parts = turns[i].parts;
    if (!parts) continue;
    for (let j = 0; j < parts.length; j++) {
      const p = parts[j];
      if (p.kind === "question" && p.questions && p.questions.length > 0 && sig(p.questions) === want) {
        // Track the last match of ANY state: an answered newest copy means the payload is
        // stale, and then nothing is hidden at all.
        ti = i;
        pi = j;
      }
    }
  }
  if (ti < 0 || turns[ti].parts![pi].answer) return none;
  // The modal blocks the turn, so nothing from the assistant can follow the pending question's
  // own line. An assistant turn after the match means the match is an older ask and the real
  // one was hidden by the Agent in its own poll — leave the history alone.
  for (let i = ti + 1; i < turns.length; i++) {
    if (turns[i].role === "assistant" && (turns[i].parts?.length || turns[i].text)) return none;
  }
  const rest = turns[ti].parts!.filter((_, j) => j !== pi);
  const next = turns.slice();
  if (rest.length === 0) {
    // The tool_use was the whole message: drop the turn rather than render an empty bubble.
    next.splice(ti, 1);
  } else {
    next[ti] = { ...turns[ti], parts: rest };
  }
  return { turns: next, hidden: true };
}
