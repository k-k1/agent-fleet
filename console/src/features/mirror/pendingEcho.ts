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

// A `/`-run slash command / skill invocation is never logged as the raw "/foo …" the
// user typed: claude records it as a `<command-name>/foo</command-name>…` user turn
// (hidden by isNoise, surfaced as a chip). So its optimistic echo can't reconcile by
// text and would sit at 「反映待ち」 forever. slashName pulls the command name out of the
// echo's first line; commandTurnName pulls it out of a transcript turn — matching the
// two (past the send anchor) lets the echo land. The leading "/" is part of the name in
// both forms, so "/" alone (length ≤ 1) is not a command.
function slashName(text: string): string | null {
  const first = (text || "").replace(/^\s+/, "").split("\n", 1)[0].trim();
  if (!first.startsWith("/")) return null;
  const name = first.split(/\s+/, 1)[0];
  return name.length > 1 ? name : null;
}
function commandTurnName(text: string): string | null {
  const s = (text || "").replace(/^\s+/, "");
  // Tag order varies by command type/CLI build: built-ins log <command-name> first,
  // skills (2.1.215 実測) log <command-message> first. Require a command tag at the
  // start (so prose merely quoting the tag can't match), then take the name wherever
  // it sits.
  if (!s.startsWith("<command-name>") && !s.startsWith("<command-message>")) return null;
  const m = s.match(/<command-name>([\s\S]*?)<\/command-name>/);
  return m && m[1].trim() ? m[1].trim() : null;
}

// echoLanded reports whether an optimistic echo's real user turn has appeared in the
// transcript. Usually the transcript line index is sufficient. For a managed native
// attachment, however, Codex can materialize the image marker in a different rollout
// item from the text, which makes that positional check miss even though the actual
// prompt is already visible. The saved attachment path is unique per upload, so use it
// as a non-positional confirmation in that case. A slash-command echo lands against its
// parsed <command-name> turn (see slashName/commandTurnName) since its text never matches.
export function echoLanded(e: PendingEcho, turns: TranscriptTurn[], isNoise: (t: TranscriptTurn) => boolean): boolean {
  const echoCmd = slashName(e.text);
  return turns.some((t) => {
    if (t.role !== "user") return false;
    // Slash-command / skill echo: reconcile against the parsed <command-name> turn
    // logged after the send. isNoise hides that turn and its text never equals the typed
    // "/foo", so the text match below can never catch it.
    if (echoCmd && t.idx !== undefined && t.idx > e.sinceIdx && commandTurnName(t.text || "") === echoCmd) return true;
    if (isNoise(t) || !samePastedPrompt(t.text || "", e.text)) return false;
    if (t.idx !== undefined && t.idx > e.sinceIdx) return true;
    return !!e.attachmentPaths?.length && e.attachmentPaths.every((path) => (t.text || "").includes(path));
  });
}
