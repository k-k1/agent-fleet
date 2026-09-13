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

// Fenced blocks and inline code are dropped before the language is judged: a Japanese answer is
// mostly English inside its code, and an English answer with a big Japanese log would read as
// Japanese. What is being asked is "is the PROSE in my language".
function proseOnly(s: string): string {
  return s
    .replace(/```[\s\S]*?```/g, " ")
    .replace(/`[^`\n]*`/g, " ")
    .replace(/^\s{4,}\S.*$/gm, " ");
}

// Kana and kanji, the only two that decide this. Latin is counted to keep a one-word answer
// ("Done.") from being offered a translation nobody wants.
const KANA = /[\u3041-\u309f\u30a0-\u30ff]/g;
const KANJI = /[\u3400-\u9fff\uf900-\ufaff]/g;
const LATIN = /[A-Za-z]/g;

const MIN_LATIN = 8;
const MIN_CJK = 8;

/**
 * Does this answer look like it is NOT in the reader's language?
 *
 * Deliberately blunt, in both directions:
 *   ja … no CJK character at all, and enough Latin letters to be prose. An answer that mixes
 *        ("実装完了。see the diff") is NOT offered: the reader can read it, and a translation
 *        would spend tokens to re-say what is already there.
 *   en … enough CJK characters to be Japanese prose.
 *
 * It only decides whether the BUTTON is offered. Nothing translates on this verdict alone, so a
 * wrong guess costs a button that should not be there, never a model run.
 */
export function looksForeign(text: string, lang: TranslateLang): boolean {
  const prose = proseOnly(text);
  const cjk = (prose.match(KANA)?.length || 0) + (prose.match(KANJI)?.length || 0);
  const latin = prose.match(LATIN)?.length || 0;
  if (lang === "ja") return cjk === 0 && latin >= MIN_LATIN;
  return cjk >= MIN_CJK;
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
