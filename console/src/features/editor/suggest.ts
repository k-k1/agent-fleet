// Structured format and pre-apply validation for AI edit suggestions (docs/log/44 §4 /
// Phase 4). The apply boundary checks the envelope identity (paneId/filePath/requestId)
// and a three-way revision match (sourceRevision === suggestion.baseRevision === the
// current bufferRevision); a mismatch is discarded as the UI code `suggestion_stale`,
// not an HTTP error (docs/log/44 §3.4). The replacement goes through the shared buffer
// validator (§1.7) twice: on its own, and as the whole applied text.

import { REVISION_RE, validateEditorBuffer, type BufferErrorCode } from "./buffer.ts";

export interface EditRange {
  /** 0-based, inclusive, UTF-16 code-unit offset. */
  from: number;
  /** 0-based, exclusive, UTF-16 code-unit offset. */
  to: number;
}

export interface EditSuggestion {
  /** Short description shown in the UI, 1-240 UTF-8 bytes. Never interpreted as an
   *  edit instruction. */
  summary: string;
  /** Replaces `range`. An empty string means a deletion. */
  replacement: string;
  range: EditRange;
  /** Revision of the text `range` was computed against. */
  baseRevision: string;
}

export interface EditSuggestionEnvelope {
  kind: "edit_suggestion";
  version: 1;
  paneId: string;
  filePath: string;
  requestId: string;
  /** bufferRevision at the time the suggestion was computed. Invalid unless equal to
   *  suggestion.baseRevision. */
  sourceRevision: string;
  suggestion: EditSuggestion;
}

/** Stable UI code for why an apply was refused. `suggestion_stale` is fixed by
 *  docs/log/44 §3.4. */
export type SuggestionIssue = "suggestion_stale" | "suggestion_invalid" | BufferErrorCode;

const SUMMARY_MAX_BYTES = 240;

const utf8Bytes = (value: string): number => new TextEncoder().encode(value).byteLength;

/** Whether `offset` points inside a surrogate pair (docs/log/44 §4.2: mid-pair is
 *  invalid). */
function splitsSurrogatePair(content: string, offset: number): boolean {
  if (offset <= 0 || offset >= content.length) return false;
  const prev = content.charCodeAt(offset - 1);
  const at = content.charCodeAt(offset);
  return prev >= 0xd800 && prev <= 0xdbff && at >= 0xdc00 && at <= 0xdfff;
}

export interface SuggestionContext {
  paneId: string;
  path: string;
  bufferRevision: string;
  content: string;
}

export type SuggestionCheck =
  | { ok: true; applied: string }
  | { ok: false; code: SuggestionIssue };

/** Validates the whole envelope against the current buffer and returns the applied text
 *  (docs/log/44 §4.2). Called both on receipt and on apply, because the buffer may have
 *  moved on in between and change the answer. */
export function checkSuggestion(
  envelope: EditSuggestionEnvelope,
  ctx: SuggestionContext,
): SuggestionCheck {
  const bad = { ok: false, code: "suggestion_invalid" } as const;
  if (envelope.kind !== "edit_suggestion" || envelope.version !== 1) return bad;
  if (!envelope.requestId) return bad;
  if (envelope.paneId !== ctx.paneId || envelope.filePath !== ctx.path) return bad;
  const s = envelope.suggestion;
  if (!REVISION_RE.test(envelope.sourceRevision) || !REVISION_RE.test(s.baseRevision)) return bad;
  if (typeof s.summary !== "string" || typeof s.replacement !== "string") return bad;
  const summaryBytes = utf8Bytes(s.summary);
  if (s.summary.trim() === "" || summaryBytes < 1 || summaryBytes > SUMMARY_MAX_BYTES) return bad;
  const { from, to } = s.range;
  if (!Number.isInteger(from) || !Number.isInteger(to)) return bad;
  if (from < 0 || to < from || to > ctx.content.length) return bad;
  if (splitsSurrogatePair(ctx.content, from) || splitsSurrogatePair(ctx.content, to)) return bad;
  if (envelope.sourceRevision !== s.baseRevision || s.baseRevision !== ctx.bufferRevision) {
    return { ok: false, code: "suggestion_stale" };
  }
  const replacementError = validateEditorBuffer(s.replacement);
  if (replacementError) return { ok: false, code: replacementError.code };
  const applied = ctx.content.slice(0, from) + s.replacement + ctx.content.slice(to);
  const appliedError = validateEditorBuffer(applied);
  if (appliedError) return { ok: false, code: appliedError.code };
  return { ok: true, applied };
}

// --- Context windows for a suggestion request (same limits as the Agent's
// editSuggestMax*) ---

export const SUGGEST_MAX_SELECTION_BYTES = 256 * 1024;
export const SUGGEST_MAX_CONTEXT_BYTES = 16 * 1024;
export const SUGGEST_MAX_INSTRUCTION_BYTES = 4 * 1024;

/** Trims from the end or the start until the value fits maxBytes, keeping code point
 *  boundaries intact. */
function clampBytes(value: string, maxBytes: number, keep: "head" | "tail"): string {
  let s = value;
  let bytes = utf8Bytes(s);
  while (bytes > maxBytes && s.length > 0) {
    // In UTF-8 one code unit is at least one byte, so dropping at least as many units as
    // the overshoot in bytes always makes progress.
    const drop = Math.max(1, Math.ceil((bytes - maxBytes) / 4));
    s = keep === "head" ? s.slice(0, s.length - drop) : s.slice(drop);
    if (keep === "head" && splitsSurrogatePair(value, s.length)) s = s.slice(0, -1);
    if (keep === "tail" && s.length > 0) {
      const first = s.charCodeAt(0);
      if (first >= 0xdc00 && first <= 0xdfff) s = s.slice(1);
    }
    bytes = utf8Bytes(s);
  }
  return s;
}

export interface SuggestWindows {
  before: string;
  selection: string;
  after: string;
}

/** Cuts the selection and its surrounding context out for the prompt. Returns null when
 *  the selection exceeds the limit (not a suggestion candidate: trimming it silently
 *  would break the correspondence between range and replacement). Context may be
 *  trimmed, and a partial line at a cut edge is dropped back to a line boundary. */
export function suggestWindows(content: string, range: EditRange): SuggestWindows | null {
  const selection = content.slice(range.from, range.to);
  if (utf8Bytes(selection) > SUGGEST_MAX_SELECTION_BYTES) return null;
  let before = clampBytes(content.slice(0, range.from), SUGGEST_MAX_CONTEXT_BYTES, "tail");
  if (before.length < range.from) {
    const nl = before.indexOf("\n");
    if (nl >= 0) before = before.slice(nl + 1);
  }
  const afterRaw = content.slice(range.to);
  let after = clampBytes(afterRaw, SUGGEST_MAX_CONTEXT_BYTES, "head");
  if (after.length < afterRaw.length) {
    const nl = after.lastIndexOf("\n");
    if (nl >= 0) after = after.slice(0, nl + 1);
  }
  return { before, selection, after };
}
