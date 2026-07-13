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

export function confirmedWorkEnd(parts: MirrorPartLike[]): number {
  let boundary = -1;
  for (let i = 0; i < parts.length; i++) {
    if (parts[i].kind === "tool" || parts[i].kind === "question" || parts[i].kind === "plan") boundary = i;
  }
  return boundary + 1;
}

// Split only when a real final text exists after the last work boundary. Questions and
// plans are tool interactions represented by dedicated part kinds; userfile is deliberately
// not a boundary because a shared file is a final deliverable and should remain visible.
export function workSplit(parts: MirrorPartLike[]): WorkSplit | null {
  const at = confirmedWorkEnd(parts);
  if (at === 0) return null;
  if (!parts.slice(at).some((p) => p.kind === "text" && !!p.text?.trim())) return null;
  const work = parts.slice(0, at);
  return {
    at,
    tools: work.filter((p) => p.kind === "tool" || p.kind === "question" || p.kind === "plan").length,
    responses: work.filter((p) => p.kind === "text" && !!p.text?.trim()).length,
  };
}

export function textOfParts(parts: MirrorPartLike[]): string {
  return parts
    .filter((p) => p.kind === "text" && !!p.text?.trim())
    .map((p) => p.text!.trim())
    .join("\n\n");
}
