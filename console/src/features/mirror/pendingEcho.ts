import { samePastedPrompt } from "../../lib/pastedImages.ts";

export interface PendingEcho {
  text: string;
  sinceIdx: number;
  // Managed drivers send these as native API attachments, so the echoed text does
  // not contain their paths. The Codex rollout does contain the paths in its image
  // marker, which gives us a more reliable reconciliation key than a line index.
  attachmentPaths?: string[];
}

interface TranscriptTurn {
  role: string;
  text?: string;
  idx?: number;
}

// echoLanded reports whether an optimistic echo's real user turn has appeared in the
// transcript. Usually the transcript line index is sufficient. For a managed native
// attachment, however, Codex can materialize the image marker in a different rollout
// item from the text, which makes that positional check miss even though the actual
// prompt is already visible. The saved attachment path is unique per upload, so use it
// as a non-positional confirmation in that case.
export function echoLanded(e: PendingEcho, turns: TranscriptTurn[], isNoise: (t: TranscriptTurn) => boolean): boolean {
  return turns.some((t) => {
    if (t.role !== "user" || isNoise(t) || !samePastedPrompt(t.text || "", e.text)) return false;
    if (t.idx !== undefined && t.idx > e.sinceIdx) return true;
    return !!e.attachmentPaths?.length && e.attachmentPaths.every((path) => (t.text || "").includes(path));
  });
}
