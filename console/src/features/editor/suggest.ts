// AI 変更提案の構造化フォーマットと適用前検証（docs/log/44 §4 / Phase 4）。
// envelope の identity（paneId/filePath/requestId）と revision の三重一致
// （sourceRevision === suggestion.baseRevision === 現在の bufferRevision）を
// 適用境界で検査し、一致しない提案は `suggestion_stale` の UI code として棄却する
// （HTTP エラーではない — docs/log/44 §3.4）。replacement は単体と適用後全文の両方を
// 共通 buffer validator（§1.7）に通す。

import { REVISION_RE, validateEditorBuffer, type BufferErrorCode } from "./buffer.ts";

export interface EditRange {
  /** 0-based, inclusive, UTF-16 code-unit offset. */
  from: number;
  /** 0-based, exclusive, UTF-16 code-unit offset. */
  to: number;
}

export interface EditSuggestion {
  /** UIに表示する1〜240 UTF-8 bytesの短い説明。変更命令として解釈しない。 */
  summary: string;
  /** range を replacement に置き換える。空文字列は削除を表す。 */
  replacement: string;
  range: EditRange;
  /** range を計算した本文の revision。 */
  baseRevision: string;
}

export interface EditSuggestionEnvelope {
  kind: "edit_suggestion";
  version: 1;
  paneId: string;
  filePath: string;
  requestId: string;
  /** 提案計算時の bufferRevision。suggestion.baseRevision と同値でなければ不正。 */
  sourceRevision: string;
  suggestion: EditSuggestion;
}

/** 適用を拒否した理由の安定 UI code。`suggestion_stale` は docs/log/44 §3.4 で固定。 */
export type SuggestionIssue = "suggestion_stale" | "suggestion_invalid" | BufferErrorCode;

const SUMMARY_MAX_BYTES = 240;

const utf8Bytes = (value: string): number => new TextEncoder().encode(value).byteLength;

/** offset が surrogate pair の内側を指すか（docs/log/44 §4.2: pair の途中は不正）。 */
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

/** envelope 全体を現在のバッファに対して検査し、適用後全文を返す（docs/log/44 §4.2）。
 *  受信時と適用時の両方で呼ぶ — 受信後にバッファが進めば結果が変わるため。 */
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

// --- 提案リクエストの文脈切り出し（Agent 側 editSuggestMax* と同じ上限） ---

export const SUGGEST_MAX_SELECTION_BYTES = 256 * 1024;
export const SUGGEST_MAX_CONTEXT_BYTES = 16 * 1024;
export const SUGGEST_MAX_INSTRUCTION_BYTES = 4 * 1024;

/** end 側/先頭側から maxBytes に収まるまで切り詰める（code point 境界を守る）。 */
function clampBytes(value: string, maxBytes: number, keep: "head" | "tail"): string {
  let s = value;
  let bytes = utf8Bytes(s);
  while (bytes > maxBytes && s.length > 0) {
    // UTF-8 は 1 code unit ≧ 1 byte なので、超過 bytes 以上の unit を落とせば必ず前進する。
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

/** 選択範囲とその前後の文脈をプロンプト用に切り出す。選択が上限を超える場合は
 *  null（提案対象外 — 黙って切り詰めると range と replacement の対応が壊れる）。
 *  文脈側は切り詰めてよく、切れた端の書きかけ行は行境界まで落とす。 */
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
