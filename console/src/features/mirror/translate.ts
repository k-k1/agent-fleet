// mirror/translate — the pure half of "read this answer in my language" (docs/log/97).
//
// The reader presses a button on ONE finished answer; the Agent runs a one-shot model outside
// the session (workspace/agent/session_translate.go) and caches the result under a hash of the
// source text. Nothing here fires on its own: the mirror polls every second, and a feature that
// translated by itself would be a model run per answer for every reader who never asked.
//
// This module holds what must not depend on React or the network: the cache key, which parts of
// a turn are translatable, and whether an answer even looks like it is in the wrong language.

import type { Group, Part } from "./transcript/types.ts";

/** Client-side ceiling for one press. Wider than the Agent's 90 s per part (session_translate.go)
 *  because a turn can hold more than one, and the request is serial on that side. */
export const TRANSLATE_TIMEOUT_MS = 240_000;

/** The languages a translation can be asked for — the Console's own display locales. */
export type TranslateLang = "ja" | "en";

const encoder = new TextEncoder();

function fnv1a32(bytes: Uint8Array, seed: number): number {
  let h = seed >>> 0;
  for (let i = 0; i < bytes.length; i++) {
    h ^= bytes[i];
    h = Math.imul(h, 16777619) >>> 0;
  }
  return h >>> 0;
}

/**
 * The cache key, and the twin of `translateHash` in workspace/agent/session_translate.go.
 * FNV-1a over the UTF-8 BYTES, twice with different offsets, hex.
 *
 * Not a cryptographic digest, because this runs while the transcript renders and the only
 * built-in digest in a browser (SubtleCrypto) is async — and absent from jsdom, which would put
 * every render test out of reach. A cache key here has to be stable and cheap, not unforgeable:
 * what it guards is a translation of text the caller is already holding.
 *
 * The bytes matter: hashing UTF-16 code units instead passes every ASCII test and then keys a
 * different entry from the Go side for every real answer. Both sides are pinned to the same
 * vectors (translate.test.ts / session_translate_test.go).
 */
export function translateHash(s: string): string {
  const bytes = encoder.encode(s);
  const a = fnv1a32(bytes, 2166136261);
  const b = fnv1a32(bytes, 2166136261 ^ 0x9e3779b9);
  return a.toString(16).padStart(8, "0") + b.toString(16).padStart(8, "0");
}

/**
 * Which language the reader reads in: the fixed answer language when they chose one
 * (Settings > 回答言語), otherwise the display language. The Agent resolves the same order for a
 * request that names none — the Console sends its own so the language the button OFFERS is the
 * one that comes back.
 */
export function targetLang(outputLanguage: string, locale: string): TranslateLang {
  if (outputLanguage === "ja" || outputLanguage === "en") return outputLanguage;
  return locale === "en" ? "en" : "ja";
}

/** The prose parts of a turn, in order. Tool traces, plans, questions, thinking and shared-file
 *  cards are not prose the reader asked to have translated, and a code block is content that has
 *  to survive verbatim — so only `kind === "text"` is sent. */
export function translatableTexts(turn: Pick<Group, "parts">): string[] {
  return (turn.parts || [])
    .filter((p: Part) => p.kind === "text" && !!p.text?.trim())
    .map((p: Part) => p.text as string);
}

/** One turn's identity for the per-turn toggle. Content-derived on purpose: a block's `idx`
 *  shifts when an older page is prepended (useStableBlockIds), and the reader would see the
 *  translation jump to a different answer. */
export function turnTranslateKey(texts: string[]): string {
  return texts.length ? translateHash(texts.join("\n\n")) : "";
}

// Fenced blocks, inline code, and quoted spans are dropped before the language is judged: a
// Japanese answer is mostly English inside its code, and an English answer with a big Japanese
// log would read as Japanese. A quoted label — citing a UI string next to its other-language
// counterpart, e.g. `"レビューする"/"Review this pull request"` or `「翻訳」/「原文」` — is
// verbatim content being pointed at, not prose in either language, and a handful of such
// characters must not by itself flip the verdict for an otherwise single-language answer. What
// is being asked is "is the PROSE in my language".
function proseOnly(s: string): string {
  return s
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`[^`\n]*`/g, " ")
    .replace(/^\s{4,}\S.*$/gm, " ")
    .replace(/"[^"\n]*"/g, " ")
    .replace(/“[^”\n]*”/g, " ")
    .replace(/「[^」\n]*」/g, " ")
    .replace(/『[^』\n]*』/g, " ");
}

// Kana and kanji, the only two that decide this. Latin is counted to keep a one-word answer
// ("Done.") from being offered a translation nobody wants.
const KANA = /[\u3041-\u309f\u30a0-\u30ff]/g;
const KANJI = /[\u3400-\u9fff\uf900-\ufaff]/g;
const LATIN = /[A-Za-z]/g;

const MIN_LATIN = 8;
const MIN_CJK = 8;

// What decides is the SHARE of the prose written in the other script, not whether a single
// character of it appears. An English answer naming a Console label in Japanese where it has no
// English name ("the engines are still 無効", "set them back to オンデマンド") is still English
// the reader cannot read — measured on one real report, those two words are eight characters in
// roughly 350 Latin letters, 2% of the prose. Below this share the stray characters are terms
// being pointed at; above it the two languages are genuinely mixed and both directions stay
// silent, because the reader can already read half of it and a translation would spend tokens
// re-saying what is there ("実装完了。see the diff").
const MAX_STRAY_SHARE = 0.05;

/**
 * Does this answer look like it is NOT in the reader's language?
 *
 * Deliberately blunt, in both directions:
 *   ja … enough Latin letters to be prose, and at most a stray word's worth of CJK.
 *   en … enough CJK characters to be Japanese prose, and more than a stray word's worth.
 *
 * It only decides whether the BUTTON is offered. Nothing translates on this verdict alone, so a
 * wrong guess costs a button that should not be there, never a model run.
 */
export function looksForeign(text: string, lang: TranslateLang): boolean {
  const prose = proseOnly(text);
  const cjk = (prose.match(KANA)?.length || 0) + (prose.match(KANJI)?.length || 0);
  const latin = prose.match(LATIN)?.length || 0;
  // No prose at all leaves the share undefined (0/0); nothing is offered for it either way.
  const cjkShare = cjk + latin > 0 ? cjk / (cjk + latin) : 0;
  if (lang === "ja") return latin >= MIN_LATIN && cjkShare <= MAX_STRAY_SHARE;
  return cjk >= MIN_CJK && cjkShare > MAX_STRAY_SHARE;
}

// Server-side per-request-part cap (session_translate.go's translateMaxPartBytes), mirrored
// here so a too-long answer can be split into several request parts BEFORE it is ever sent,
// rather than only after the server has already said no. The 32 KiB value itself is not load-
// bearing for correctness (a stale copy only shifts where splitting kicks in, never breaks it),
// so unlike translateHash this does not need a pinned cross-language vector.
export const TRANSLATE_MAX_PART_BYTES = 32 * 1024;

function byteLen(s: string): number {
  return encoder.encode(s).length;
}

// Character-offset ranges of ```-fenced blocks (opening line through the matching closing
// line), so a split can avoid landing between them. An unterminated fence counts as open to the
// end of the text — safer than treating a stray ``` as ordinary prose and cutting through it.
function fenceRanges(text: string): Array<[number, number]> {
  const ranges: Array<[number, number]> = [];
  const re = /^```.*$/gm;
  let m: RegExpExecArray | null;
  let openAt = -1;
  while ((m = re.exec(text))) {
    if (openAt < 0) openAt = m.index;
    else {
      ranges.push([openAt, m.index + m[0].length]);
      openAt = -1;
    }
  }
  if (openAt >= 0) ranges.push([openAt, text.length]);
  return ranges;
}

const insideFence = (offset: number, ranges: Array<[number, number]>): boolean =>
  ranges.some(([start, end]) => offset > start && offset < end);

// Offsets right after each run of `pattern`, skipping any that fall inside a fenced block — a
// fence's opening and closing ``` must never end up split across two request parts unless the
// fence alone is already over the limit.
function breakOffsets(text: string, pattern: RegExp, ranges: Array<[number, number]>): number[] {
  const out: number[] = [];
  const re = new RegExp(pattern.source, "g");
  let m: RegExpExecArray | null;
  while ((m = re.exec(text))) {
    const offset = m.index + m[0].length;
    if (!insideFence(offset, ranges)) out.push(offset);
  }
  return out;
}

// The largest candidate offset (candidates ascending) that is past `start` and keeps
// text.slice(start, offset) within maxBytes. The slice only grows as offset increases, so byte
// length is monotonic — the first candidate that no longer fits ends the search.
function bestCut(text: string, start: number, maxBytes: number, candidates: number[]): number | undefined {
  let best: number | undefined;
  for (const c of candidates) {
    if (c <= start) continue;
    if (byteLen(text.slice(start, c)) <= maxBytes) best = c;
    else break;
  }
  return best;
}

// Last resort: a byte-safe cut (never inside a UTF-8 multi-byte sequence) at the widest prefix
// that still fits. Always makes progress: even one character's UTF-8 encoding is far under any
// maxBytes this runs with.
function hardCut(text: string, start: number, maxBytes: number): number {
  let lo = start + 1;
  let hi = text.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (byteLen(text.slice(start, mid)) <= maxBytes) lo = mid;
    else hi = mid - 1;
  }
  // Never land between a UTF-16 surrogate pair (an emoji, most CJK beyond the BMP): text.slice
  // there is not actually "byte-safe" despite fitting the byte count above — TextEncoder (and
  // the real UTF-8 encode a fetch body goes through) turns each lone surrogate half into its
  // OWN U+FFFD, so the character reaching the server is not reassembled, it is destroyed into
  // two replacement characters. Move the cut before the pair; if that leaves nothing to emit
  // (only possible with a maxBytes of a handful of bytes, never a real request), emit the pair
  // whole instead — one character slightly over the cap beats a corrupted one.
  const before = text.charCodeAt(lo - 1);
  if (before >= 0xd800 && before <= 0xdbff) lo = lo - 1 > start ? lo - 1 : lo + 1;
  return lo;
}

// A sentence boundary: Latin-style closing punctuation followed by a space or tab (". ", "; ",
// "! ", "? "), or a Japanese sentence-ending mark, which needs no trailing space of its own.
// This is the last STRUCTURAL boundary tried before giving up to a raw byte cut — it is what
// saves a long line that has no newline in it at all (a table row, a wrapped log line, one
// unbroken paragraph) from being cut mid-word.
const SENTENCE_BREAK = /[.!?;][ \t]|[。、！？]/;

/**
 * Splits one translate request part at the best available boundary once it is over the
 * server's per-part cap: a blank line, then a single line break, then a sentence boundary, then
 * a raw byte-safe cut, in that preference order. Concatenating the returned chunks with NO
 * separator reproduces `text` exactly (each chunk keeps its own trailing newlines), so the
 * caller sends them as independent request parts and joins the translated replies back in the
 * same order.
 *
 * A fenced code block is kept whole whenever it fits in one chunk on its own: splitting through
 * the middle of a ``` pair would hand each half to the model as a separate request, and the
 * persona's "keep code fences verbatim" instruction cannot save a fence that is already broken
 * before translation starts.
 */
export function splitForTranslate(text: string, maxBytes: number = TRANSLATE_MAX_PART_BYTES): string[] {
  if (byteLen(text) <= maxBytes) return [text];
  const ranges = fenceRanges(text);
  const blank = breakOffsets(text, /\n{2,}/, ranges);
  const single = breakOffsets(text, /\n/, ranges);
  const sentence = breakOffsets(text, SENTENCE_BREAK, ranges);
  const out: string[] = [];
  let start = 0;
  while (start < text.length) {
    const end =
      bestCut(text, start, maxBytes, blank) ??
      bestCut(text, start, maxBytes, single) ??
      bestCut(text, start, maxBytes, sentence) ??
      hardCut(text, start, maxBytes);
    out.push(text.slice(start, end));
    start = end;
  }
  return out;
}

/** POST /api/sessions/{name}/translate — one entry per requested part, in request order. */
export interface TranslatePartReply {
  hash: string;
  text: string;
  /** true = it was already held, so the press cost nothing. The reply says nothing about WHICH
   *  model ran: only the Agent's ledger knows that (session_translate.go explains why). */
  cached: boolean;
}

export interface TranslateReply {
  lang: TranslateLang;
  parts: TranslatePartReply[];
}

/** GET /api/sessions/{name}/translations — source hash -> translation, for one language. */
export interface TranslationsReply {
  lang: TranslateLang;
  entries: Record<string, string>;
}
