import { samePastedPrompt } from "../../lib/pastedImages.ts";

export interface PendingEcho {
  text: string;
  sinceIdx: number;
  // Managed drivers send these as native API attachments, so the echoed text does
  // not contain their paths. The Codex rollout does contain the paths in its image
  // marker, which gives us a more reliable reconciliation key than a line index.
  attachmentPaths?: string[];
  /** Send time (ms) — the clock echoNeedsResync measures the stuck window against. */
  at?: number;
  /** When a stuck echo already forced a full transcript re-read (ms); one shot. */
  resyncedAt?: number;
}

// How long an echo may sit at "Pending" on an IDLE session before we suspect our own
// copy of the transcript is incomplete rather than the send being slow.
export const ECHO_RESYNC_MS = 20000;

// echoNeedsResync reports whether a still-pending echo should trigger one full re-read
// of the transcript (cursor → 0, i.e. the initial tail window again).
//
// Reconciliation matches an echo against the turns the Console HOLDS, so a turn that
// never reached us can strand its echo forever — as when the Agent handed out a cursor
// past a half-written transcript line (see readJSONLLines) and the client, which only
// ever asks for lines after its cursor, could not ask for that turn again. A re-read is
// cheap (one windowed fetch), fixes the hole, and then the echo lands on its own; the
// one-shot flag keeps a genuinely undelivered prompt from re-reading every poll.
export function echoNeedsResync(e: PendingEcho, nowMs: number): boolean {
  if (!e.at || e.resyncedAt) return false;
  return nowMs - e.at >= ECHO_RESYNC_MS;
}

interface TranscriptTurn {
  role: string;
  text?: string;
  idx?: number;
  parts?: { kind: string }[];
}

// hasErrorPart reports whether a turn carries a kind="error" part (opencode's provider
// failure, or codex's synthetic turn-rejection turn — see errors.go / managedEnrich).
function hasErrorPart(t: TranscriptTurn): boolean {
  return !!t.parts?.some((p) => p.kind === "error");
}

// A `/`-run slash command / skill invocation is never logged as the raw "/foo …" the
// user typed: claude records it as a `<command-name>/foo</command-name>…` user turn
// (hidden by isNoise, surfaced as a chip). So its optimistic echo can't reconcile by
// text and would sit at "Pending" forever. slashName pulls the command name out of the
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
  // skills (measured on 2.1.215) log <command-message> first. Require a command tag at the
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
    if (t.role !== "user") {
      // A turn rejected before codex ever creates one (e.g. a usage-limit-exhausted
      // send) never gets its own echoed user turn — not even the prompt is recorded —
      // so the text/attachment match below can never fire and the echo would sit at
      // "Pending" forever. Any error turn landing after the send explains what happened
      // to it, so treat it as the resolution too (driver.go's managedEnrich / opencode's
      // errors.go both emit kind="error" for exactly this).
      return t.idx !== undefined && t.idx > e.sinceIdx && hasErrorPart(t);
    }
    // Slash-command / skill echo: reconcile against the parsed <command-name> turn
    // logged after the send. isNoise hides that turn and its text never equals the typed
    // "/foo", so the text match below can never catch it.
    if (echoCmd && t.idx !== undefined && t.idx > e.sinceIdx && commandTurnName(t.text || "") === echoCmd) return true;
    if (isNoise(t) || !samePastedPrompt(t.text || "", e.text)) return false;
    if (t.idx !== undefined && t.idx > e.sinceIdx) return true;
    return !!e.attachmentPaths?.length && e.attachmentPaths.every((path) => (t.text || "").includes(path));
  });
}
