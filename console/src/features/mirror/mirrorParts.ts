// Pure ordered-part helpers for the session mirror. The UI and tests use this instead of
// inferring the final-answer boundary from rendered DOM or poll timing.

export interface MirrorPartLike {
  kind: string;
  text?: string;
}

export interface WorkSplit {
  at: number; // first part of the final-answer section
  tools: number;
  responses: number;
}

export interface PromptBoundaryLike {
  role: string;
  queued?: boolean;
}

// The user turn just before the work to explore begins. A just-sent optimistic echo (pending)
// also bounds the current work, but a queued prompt that has not run yet does not.
export function latestWorkPromptIndex(groups: PromptBoundaryLike[]): number {
  for (let i = groups.length - 1; i >= 0; i--) {
    if (groups[i].role === "user" && !groups[i].queued) return i;
  }
  return -1;
}

// awaitingReply: the newest user prompt has no assistant reply after it yet — the mirror
// is still waiting for the answer to the latest turn. Used to hold the "working" indicator
// across the gap between a session reading idle and its reply actually landing in the
// transcript (the reply is the first non-user group after the last user group). Returns
// false when there's no prompt at all (a fresh or history-only view has nothing pending).
export function awaitingReply(groups: PromptBoundaryLike[]): boolean {
  let u = -1;
  for (let i = groups.length - 1; i >= 0; i--) {
    if (groups[i].role === "user") { u = i; break; }
  }
  if (u < 0) return false;
  const reply = groups[u + 1];
  return !(reply && reply.role !== "user");
}

export function confirmedWorkEnd(parts: MirrorPartLike[]): number {
  let boundary = -1;
  for (let i = 0; i < parts.length; i++) {
    if (parts[i].kind === "tool" || parts[i].kind === "question" || parts[i].kind === "plan") boundary = i;
  }
  return boundary + 1;
}

const isToolLike = (p: MirrorPartLike): boolean =>
  p.kind === "tool" || p.kind === "question" || p.kind === "plan";
const isText = (p: MirrorPartLike): boolean => p.kind === "text" && !!p.text?.trim();
const textLen = (p: MirrorPartLike): number => (isText(p) ? p.text!.trim().length : 0);

// Split only when a real final text exists after the last work boundary. Questions and
// plans are tool interactions represented by dedicated part kinds; userfile is deliberately
// not a boundary because a shared file is a final deliverable and should remain visible.
//
// The naive boundary is "after the last tool", but a post-answer housekeeping action — the
// agent gives its real answer, then writes a memory note (a Write/Edit tool) and adds a short
// sign-off — pushes the boundary past the real answer, folding it away and leaving only the
// sign-off as the "final answer" (both on screen and for TTS). So: take the last-tool
// boundary, then, if the longest text BEFORE it is longer than the whole tail after it, that
// earlier text is the dominant/real answer and the trailing tool(s) are housekeeping — move
// the boundary onto that answer, keeping the housekeeping tool and sign-off in the visible/read
// final answer. A tail at least as long as anything before it is the genuine answer, so the
// boundary stays put and a normal "narrate → tool → answer" turn folds exactly as before.
export function workSplit(parts: MirrorPartLike[]): WorkSplit | null {
  let at = confirmedWorkEnd(parts);
  if (at === 0) return null;
  const tailLen = textOfParts(parts.slice(at)).length;
  if (tailLen === 0) return null; // no final answer yet (turn still working / tools only)

  // Dominant answer before the boundary = the longest text part (leftmost on ties, so the
  // whole final-answer region stays visible). Move the boundary there when it beats the tail.
  let bestLen = 0;
  let bestIdx = -1;
  for (let i = 0; i < at; i++) {
    if (textLen(parts[i]) > bestLen) {
      bestLen = textLen(parts[i]);
      bestIdx = i;
    }
  }
  if (bestIdx >= 0 && bestLen > tailLen) at = bestIdx;
  if (at === 0) return null; // the real answer is the whole turn — nothing to fold

  const work = parts.slice(0, at);
  const tools = work.filter(isToolLike).length;
  if (!tools) return null;
  return { at, tools, responses: work.filter(isText).length };
}

export function textOfParts(parts: MirrorPartLike[]): string {
  return parts
    .filter((p) => p.kind === "text" && !!p.text?.trim())
    .map((p) => p.text!.trim())
    .join("\n\n");
}
